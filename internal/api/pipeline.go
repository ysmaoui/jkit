package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// defaultStageLogCap bounds how many bytes of a stage log one read keeps in
// memory or, when following without step logs, reads at all. Neither stage log
// endpoint takes a start offset: PGV's stages/log and Blue Ocean's
// nodes/<id>/log/ both write every step from byte 0, so reading past the cap
// means downloading the head.
const defaultStageLogCap = 10 << 20 // 10 MB

// ErrStageLogUnavailable means no stage log endpoint answered for the build:
// neither plugin is installed, or the build itself does not exist.
var ErrStageLogUnavailable = errors.New("blue ocean plugin required for stage logs")

// ErrStageLogPerStep means the server serves the stage log only step by step.
// The concatenation is not append-only: the newline added after a step whose
// log does not end in one moves once that step writes more, so a byte offset
// into it does not stay put between reads and the log cannot be followed.
var ErrStageLogPerStep = errors.New("stage log is only available per step on this server")

// pgvNoLogs is the whole body PGV's stages/log sends for a node with no step
// logs yet. It is not log text, so it must not advance a follow offset.
const pgvNoLogs = "No logs found\n"

// GetPipelineStages returns the flat stage list for a build. It prefers the
// Pipeline Graph View plugin (`/stages/tree`, v803+) and falls back to Blue
// Ocean (`/blue/rest/.../nodes/`) on 404 or when the client is pinned to
// Blue Ocean via JKIT_PIPELINE_SOURCE / WithPipelineSource.
//
// nil, nil means the build has no stage data (not a pipeline, or neither
// plugin answers). A pipeline with no stages yet gets an empty non-nil slice.
func (c *Client) GetPipelineStages(jobPath string, number int) ([]jenkins.Stage, error) {
	if c.pipelineSource != PipelineSourceBlueOcean {
		stages, err := c.getPipelineStagesPGV(jobPath, number)
		if err == nil {
			return stages, nil
		}
		var nfe *jenkins.NotFoundError
		if !errors.As(err, &nfe) || c.pipelineSource == PipelineSourcePGV {
			return nil, err
		}
		// 404 on PGV in auto mode → fall back to Blue Ocean
	}
	if c.pipelineSource == PipelineSourcePGV {
		return nil, fmt.Errorf("PGV endpoint unavailable and fallback disabled")
	}
	return c.getPipelineStagesBlueOcean(jobPath, number)
}

func (c *Client) getPipelineStagesPGV(jobPath string, number int) ([]jenkins.Stage, error) {
	path := fmt.Sprintf("%s/%d/stages/tree", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var pgv jenkins.PGVResponse
	if err := json.NewDecoder(resp.Body).Decode(&pgv); err != nil {
		return nil, fmt.Errorf("decoding PGV tree: %w", err)
	}
	if pgv.Status != "ok" {
		return nil, fmt.Errorf("PGV status %q", pgv.Status)
	}
	// Non-nil even when empty: nil from GetPipelineStages means "no stage
	// data", and a pipeline that has not entered its first stage is not that.
	stages := jenkins.FlattenPGVTree(pgv.Data.Stages)
	if stages == nil {
		stages = []jenkins.Stage{}
	}
	return stages, nil
}

func (c *Client) getPipelineStagesBlueOcean(jobPath string, number int) ([]jenkins.Stage, error) {
	segments := normalizeBluePath(jobPath)
	path := fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/", segments, number)

	resp, err := c.Get(path, url.Values{"limit": {"10000"}})
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, nil // Blue Ocean not available or not a pipeline
		}
		return nil, fmt.Errorf("getting pipeline stages: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var stages []jenkins.Stage
	if err := json.NewDecoder(resp.Body).Decode(&stages); err != nil {
		return nil, fmt.Errorf("decoding stages: %w", err)
	}
	return stages, nil
}

// GetStageLog returns the first StageLogCap bytes of a pipeline node's log.
// truncated reports that the log is longer.
func (c *Client) GetStageLog(jobPath string, number int, nodeID string) (text string, truncated bool, err error) {
	body, err := c.openStageLog(jobPath, number, nodeID)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = body.Close() }()

	data, err := io.ReadAll(io.LimitReader(body, int64(c.stageLogCap)+1))
	if err != nil {
		return "", false, fmt.Errorf("reading stage log: %w", err)
	}
	if len(data) > c.stageLogCap {
		return string(data[:c.stageLogCap]), true, nil
	}
	return string(data), false, nil
}

// CopyStageLogFrom copies a pipeline node's log from byte offset start up to
// StageLogCap to w and returns the bytes written. No call reads more than the
// cap plus one byte; capped reports that the log runs past the cap.
func (c *Client) CopyStageLogFrom(jobPath string, number int, nodeID string, start int64, w io.Writer) (n int64, capped bool, err error) {
	body, err := c.openStageLog(jobPath, number, nodeID)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = body.Close() }()
	if _, ok := body.(*stepLogReader); ok {
		return 0, false, ErrStageLogPerStep
	}

	limit := int64(c.stageLogCap)
	lr := &io.LimitedReader{R: body, N: limit + 1}
	if _, err := io.CopyN(io.Discard, lr, start); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("reading stage log: %w", err)
	}
	src := io.LimitReader(lr, limit-start)
	if start == 0 {
		head := make([]byte, len(pgvNoLogs)+1)
		k, err := io.ReadFull(src, head)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, false, fmt.Errorf("reading stage log: %w", err)
		}
		if string(head[:k]) == pgvNoLogs {
			return 0, false, nil
		}
		src = io.MultiReader(bytes.NewReader(head[:k]), src)
	}
	n, err = io.Copy(w, src)
	if err != nil {
		return n, false, fmt.Errorf("reading stage log: %w", err)
	}
	var past [1]byte
	m, _ := io.ReadFull(lr, past[:])
	return n, m == 1, nil
}

// GetStageLogTail returns the end of a pipeline node's log, at most
// StageLogCap bytes. The endpoints cannot seek, so the whole log is downloaded
// through a ring buffer of one window. truncated reports that the head was
// dropped; the partial first line is then dropped too.
//
// If the download fails after the log opened, err is set and text still holds
// the end of what arrived before the failure, so a caller that can use part of
// the log need not download it again. text is empty only when nothing arrived.
func (c *Client) GetStageLogTail(jobPath string, number int, nodeID string) (text string, truncated bool, err error) {
	body, err := c.openStageLog(jobPath, number, nodeID)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = body.Close() }()

	ring, err := io.ReadAll(io.LimitReader(body, int64(c.stageLogCap)))
	if err != nil {
		return string(ring), false, fmt.Errorf("reading stage log: %w", err)
	}
	if len(ring) < c.stageLogCap {
		return string(ring), false, nil
	}
	var readErr error
	pos := 0
	for {
		n, err := body.Read(ring[pos:])
		if n > 0 {
			truncated = true
			pos = (pos + n) % len(ring)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = fmt.Errorf("reading stage log: %w", err)
			break
		}
	}
	if !truncated {
		return string(ring), false, readErr
	}
	// Rotate in place so the oldest byte comes first, without a second window.
	slices.Reverse(ring[:pos])
	slices.Reverse(ring[pos:])
	slices.Reverse(ring)
	// Kept whole when its only newline is the last byte: dropping would leave
	// nothing, and callers read empty text as "nothing arrived".
	if i := bytes.IndexByte(ring, '\n'); i >= 0 && i < len(ring)-1 {
		ring = ring[i+1:]
	}
	return string(ring), true, readErr
}

// openStageLog opens a pipeline node's log. It prefers the PGV endpoint
// (`/stages/log?nodeId=...`, which also serves step IDs) and falls back to
// Blue Ocean on 404. Blue Ocean parallel containers that return 500 on node
// log are read step by step instead.
func (c *Client) openStageLog(jobPath string, number int, nodeID string) (io.ReadCloser, error) {
	if c.pipelineSource != PipelineSourceBlueOcean {
		path := fmt.Sprintf("%s/%d/stages/log", NormalizeJobPath(jobPath), number)
		resp, err := c.Get(path, url.Values{"nodeId": {nodeID}})
		if err == nil {
			return resp.Body, nil
		}
		var nfe *jenkins.NotFoundError
		if !errors.As(err, &nfe) || c.pipelineSource == PipelineSourcePGV {
			return nil, err
		}
		// 404 → fall through to Blue Ocean
	}
	if c.pipelineSource == PipelineSourcePGV {
		return nil, fmt.Errorf("PGV endpoint unavailable and fallback disabled")
	}

	segments := normalizeBluePath(jobPath)
	path := fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/%s/log/", segments, number, nodeID)
	resp, err := c.Get(path, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, ErrStageLogUnavailable
		}
		var se *jenkins.ServerError
		if errors.As(err, &se) {
			steps, err := c.openStageLogViaSteps(segments, number, nodeID)
			if err != nil {
				return nil, err
			}
			return steps, nil
		}
		return nil, fmt.Errorf("getting stage log: %w", err)
	}
	return resp.Body, nil
}

// stepsPageSize is how many steps one listing request asks for. Blue Ocean's
// @PagedResponse defaults to 100 and honors start= and limit=.
var stepsPageSize = 10000

// openStageLogViaSteps lists a stage's steps and returns a reader over their
// logs in order. Used when Blue Ocean's node-level /log/ returns 500.
func (c *Client) openStageLogViaSteps(blueSegments string, number int, nodeID string) (*stepLogReader, error) {
	prefix := blueStepsPrefix(blueSegments, number, nodeID)
	steps, err := c.listBlueSteps(context.Background(), prefix)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps found for stage")
	}
	r := &stepLogReader{c: c, prefix: prefix}
	for _, s := range steps {
		r.ids = append(r.ids, s.ID)
	}
	return r, nil
}

func blueStepsPrefix(blueSegments string, number int, nodeID string) string {
	return fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/%s/steps/", blueSegments, number, nodeID)
}

// blueStep is one entry of Blue Ocean's step listing.
type blueStep struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func (c *Client) listBlueSteps(ctx context.Context, prefix string) ([]blueStep, error) {
	var steps []blueStep
	seen := map[string]bool{}
	for {
		page, err := c.listBlueStepsPage(ctx, prefix, len(steps))
		if err != nil {
			return nil, err
		}
		// A short page is not the end: a server may clamp limit. One that
		// ignores start= repeats the first page instead of running dry.
		if len(page) == 0 || seen[page[0].ID] {
			return steps, nil
		}
		for _, s := range page {
			seen[s.ID] = true
		}
		steps = append(steps, page...)
	}
}

func (c *Client) listBlueStepsPage(ctx context.Context, prefix string, start int) ([]blueStep, error) {
	resp, err := c.getContext(ctx, prefix, url.Values{"start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(stepsPageSize)}}, nil)
	if err != nil {
		return nil, fmt.Errorf("getting stage steps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var steps []blueStep
	if err := json.NewDecoder(resp.Body).Decode(&steps); err != nil {
		return nil, fmt.Errorf("decoding stage steps: %w", err)
	}
	return steps, nil
}

// stepLogReader concatenates step logs, opening each only once the previous
// one is drained. Callers read through their own cap, so a capped read fetches
// only the steps it needs and memory stays within the caller's window.
//
// Each step log is requested with start=0: without it Blue Ocean's LogResource
// sends only the last 150 KB (DEFAULT_LOG_THRESHOLD) of the step.
type stepLogReader struct {
	c       *Client
	prefix  string
	ids     []string
	cur     io.ReadCloser
	curID   string
	pending []byte
	last    byte
	any     bool
}

func (r *stepLogReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			r.last = p[n-1]
			r.any = true
			return n, nil
		}
		if r.cur == nil {
			if len(r.ids) == 0 {
				if !r.any {
					return 0, fmt.Errorf("no log content from stage steps")
				}
				return 0, io.EOF
			}
			id := r.ids[0]
			r.ids = r.ids[1:]
			resp, err := r.c.Get(r.prefix+url.PathEscape(id)+"/log/", url.Values{"start": {"0"}})
			if err != nil {
				// A step that never wrote output has no log.
				var nfe *jenkins.NotFoundError
				if errors.As(err, &nfe) {
					continue
				}
				// Every other step would fail the same way, and an unreachable
				// server has already used up its retries on this one.
				var ae *jenkins.AuthError
				var pe *jenkins.PermissionError
				var ue *jenkins.UnreachableError
				if errors.As(err, &ae) || errors.As(err, &pe) || errors.As(err, &ue) {
					return 0, err
				}
				// Say what is missing in place rather than lose the other steps.
				r.pending = fmt.Appendf(r.pending, "[jkit: step %s log unavailable: %s]\n", id, shortErr(err))
				continue
			}
			r.cur = resp.Body
			r.curID = id
		}
		n, err := r.cur.Read(p)
		if n > 0 {
			r.any = true
			r.last = p[n-1]
		}
		if err != nil {
			_ = r.cur.Close()
			r.cur = nil
			if r.any && r.last != '\n' {
				r.pending = append(r.pending, '\n')
			}
			// A step cut off mid-body (truncated response, read timeout) is
			// marked like one that failed to open, so later steps still arrive.
			if !errors.Is(err, io.EOF) {
				r.pending = fmt.Appendf(r.pending, "[jkit: step %s log incomplete: %s]\n", r.curID, shortErr(err))
			}
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (r *stepLogReader) Close() error {
	if r.cur == nil {
		return nil
	}
	return r.cur.Close()
}

// shortErr is err in one line, for a marker inside log text.
func shortErr(err error) string {
	var se *jenkins.ServerError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d", se.StatusCode)
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return msg
}

// normalizeBluePath converts "team/svc" to "team/pipelines/svc" with URL-encoded segments.
func normalizeBluePath(natural string) string {
	parts := strings.Split(strings.Trim(natural, "/"), "/")
	if len(parts) <= 1 {
		return url.PathEscape(strings.Trim(natural, "/"))
	}
	result := url.PathEscape(parts[0])
	for _, p := range parts[1:] {
		result += "/pipelines/" + url.PathEscape(p)
	}
	return result
}
