package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ContainerHint returns a ContainerBuildError if jobPath is actually a folder or
// multibranch pipeline (neither has builds of its own), listing its child jobs so
// the caller can retry with the right target. It returns nil if jobPath is a
// normal job or cannot be inspected. Callers use it to turn a bare 404 or an
// empty result into actionable guidance.
func (c *Client) ContainerHint(jobPath string) *jenkins.ContainerBuildError {
	job, err := c.inspectContainer(jobPath)
	if err != nil || job == nil || !job.IsContainer() {
		return nil
	}
	kind := "folder"
	if job.IsMultibranch() {
		kind = "multibranch pipeline"
	}
	children := make([]string, 0, len(job.Jobs))
	for _, child := range job.Jobs {
		children = append(children, child.Name)
	}
	return &jenkins.ContainerBuildError{
		JobPath:  jobPath,
		Kind:     kind,
		Children: children,
		Host:     c.host,
	}
}

// enrichNotFound upgrades a NotFoundError on a build request into a
// ContainerBuildError when jobPath is really a container. Any other error (or a
// normal job) is returned unchanged.
func (c *Client) enrichNotFound(jobPath string, err error) error {
	var nfe *jenkins.NotFoundError
	if !errors.As(err, &nfe) {
		return err
	}
	if hint := c.ContainerHint(jobPath); hint != nil {
		return hint
	}
	return err
}

func (c *Client) GetBuilds(jobPath string, limit int) ([]jenkins.Build, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	tree := fmt.Sprintf("builds[number,result,timestamp,duration,building]{0,%d}", limit)
	query := url.Values{"tree": {tree}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting builds: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Builds []jenkins.Build `json:"builds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding builds: %w", err)
	}
	return result.Builds, nil
}

func (c *Client) GetBuild(jobPath string, number int) (*jenkins.Build, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	query := url.Values{"tree": {"number,result,timestamp,duration,building,url,actions[_class,parameters[name,value],causes[shortDescription,_class]],changeSets[items[commitId,msg,author[fullName],timestamp]]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var build jenkins.Build
	if err := json.NewDecoder(resp.Body).Decode(&build); err != nil {
		return nil, fmt.Errorf("decoding build: %w", err)
	}
	return &build, nil
}

func (c *Client) TriggerBuild(jobPath string, params map[string]string) (int, error) {
	path := NormalizeJobPath(jobPath)
	var body io.Reader
	contentType := ""

	if len(params) > 0 {
		path += "/buildWithParameters"
		form := url.Values{}
		for k, v := range params {
			form.Set(k, v)
		}
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else {
		path += "/build"
	}

	resp, err := c.Post(path, body, contentType)
	if err != nil {
		return 0, fmt.Errorf("triggering build: %w", err)
	}
	defer CloseBody(resp)

	loc := resp.Header.Get("Location")
	if loc == "" {
		return 0, fmt.Errorf("no queue item returned — Jenkins did not provide a Location header")
	}
	// Parse queue item ID from Location header: .../queue/item/123/
	parts := strings.Split(strings.TrimRight(loc, "/"), "/")
	if len(parts) == 0 {
		return 0, fmt.Errorf("could not parse queue item from Location: %s", loc)
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, fmt.Errorf("could not parse queue item ID from Location %q: %w", loc, err)
	}
	return id, nil
}

func (c *Client) StopBuild(jobPath string, number int) error {
	path := fmt.Sprintf("%s/%d/stop", NormalizeJobPath(jobPath), number)
	resp, err := c.Post(path, nil, "")
	if err != nil {
		return fmt.Errorf("stopping build: %w", err)
	}
	CloseBody(resp)
	return nil
}

// maxLogChunk bounds how many bytes GetBuildLog reads per request. Jenkins'
// progressiveText streams the entire log from `start` in one response, so
// without this cap a multi-hundred-MB console would be buffered whole. Callers
// page by feeding the returned Offset back in as start.
const maxLogChunk = 10 << 20 // 10 MB per request

// GetBuildLog fetches a chunk of the console log starting at byte offset `start`.
// It reads at most maxLogChunk bytes and reports Offset as the byte position
// actually reached (start + bytes read) — NOT the server's total size. HasMore
// is true while the build is still producing output (X-More-Data) OR unread
// bytes remain (Offset < X-Text-Size), so paging on Offset walks the entire log
// even when it exceeds the per-request cap. (Previously Offset was set to
// X-Text-Size and HasMore to X-More-Data alone, so a completed build larger
// than the cap was silently truncated to its first chunk.)
func (c *Client) GetBuildLog(jobPath string, number int, start int64) (*jenkins.LogChunk, error) {
	path := fmt.Sprintf("%s/%d/logText/progressiveText", NormalizeJobPath(jobPath), number)
	query := url.Values{"start": {strconv.FormatInt(start, 10)}}

	resp, err := c.Get(path, query)
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting build log: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	text, err := io.ReadAll(io.LimitReader(resp.Body, maxLogChunk))
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}

	offset := start + int64(len(text))

	hasMore := resp.Header.Get("X-More-Data") == "true"
	if sz := resp.Header.Get("X-Text-Size"); sz != "" {
		if total, err := strconv.ParseInt(sz, 10, 64); err == nil && total > offset {
			// Unread bytes remain — e.g. a completed build whose log exceeds the
			// per-request cap, where X-More-Data is absent.
			hasMore = true
		}
	}

	return &jenkins.LogChunk{
		Text:    string(text),
		Offset:  offset,
		HasMore: hasMore,
	}, nil
}

// GetBuildLogSize returns the current total byte size of the console log via the
// X-Text-Size header, without downloading the body. Used to locate the tail of
// large logs cheaply.
func (c *Client) GetBuildLogSize(jobPath string, number int) (int64, error) {
	path := fmt.Sprintf("%s/%d/logText/progressiveText", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"start": {"0"}})
	if err != nil {
		return 0, fmt.Errorf("getting log size: %w", err)
	}
	// Close without draining: we only need the header, not the (potentially huge)
	// body. Closing aborts the transfer.
	defer func() { _ = resp.Body.Close() }()

	sz := resp.Header.Get("X-Text-Size")
	if sz == "" {
		return 0, fmt.Errorf("server did not report X-Text-Size")
	}
	n, err := strconv.ParseInt(sz, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing X-Text-Size %q: %w", sz, err)
	}
	return n, nil
}

// GetBuildLogTail returns up to the last maxBytes of the console log, trimming a
// partial leading line when the window starts mid-stream. It probes the size,
// then pages from the window start to the end so a window larger than the
// per-request cap is still fully read.
func (c *Client) GetBuildLogTail(jobPath string, number int, maxBytes int64) (string, error) {
	size, err := c.GetBuildLogSize(jobPath, number)
	if err != nil {
		return "", err
	}

	start := int64(0)
	if maxBytes > 0 && size > maxBytes {
		start = size - maxBytes
	}

	var buf strings.Builder
	for off := start; off < size; {
		chunk, err := c.GetBuildLog(jobPath, number, off)
		if err != nil {
			return "", err
		}
		buf.WriteString(chunk.Text)
		if chunk.Offset <= off {
			break // no forward progress; avoid looping forever
		}
		off = chunk.Offset
		if !chunk.HasMore {
			break
		}
	}

	text := buf.String()
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text, nil
}
