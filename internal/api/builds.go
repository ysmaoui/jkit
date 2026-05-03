package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/ysmaoui/jk/internal/jenkins"
)

func (c *Client) GetBuilds(jobPath string, limit int) ([]jenkins.Build, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	tree := fmt.Sprintf("builds[number,result,timestamp,duration,building]{0,%d}", limit)
	query := url.Values{"tree": {tree}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting builds: %w", err)
	}
	defer resp.Body.Close()

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
		return nil, fmt.Errorf("getting build: %w", err)
	}
	defer resp.Body.Close()

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

func (c *Client) GetBuildLog(jobPath string, number int, start int64) (*jenkins.LogChunk, error) {
	path := fmt.Sprintf("%s/%d/logText/progressiveText", NormalizeJobPath(jobPath), number)
	query := url.Values{"start": {strconv.FormatInt(start, 10)}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting build log: %w", err)
	}
	defer resp.Body.Close()

	const maxLogChunk = 10 << 20 // 10 MB per chunk
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxLogChunk))
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}

	offset := start
	if sz := resp.Header.Get("X-Text-Size"); sz != "" {
		if n, err := strconv.ParseInt(sz, 10, 64); err == nil {
			offset = n
		}
	}

	hasMore := resp.Header.Get("X-More-Data") == "true"

	return &jenkins.LogChunk{
		Text:    string(text),
		Offset:  offset,
		HasMore: hasMore,
	}, nil
}
