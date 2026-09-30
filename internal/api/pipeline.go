package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// defaultStageLogCap bounds how many bytes of a stage log one read keeps in
// memory or, when following, reads at all. Neither stage log endpoint takes a
// start offset: PGV's stages/log and Blue Ocean's nodes/<id>/log/ both write
// every step from byte 0, so reading past the cap means downloading the head.
const defaultStageLogCap = 10 << 20 // 10 MB

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
func (c *Client) GetStageLogTail(jobPath string, number int, nodeID string) (text string, truncated bool, err error) {
	body, err := c.openStageLog(jobPath, number, nodeID)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = body.Close() }()

	ring, err := io.ReadAll(io.LimitReader(body, int64(c.stageLogCap)))
	if err != nil {
		return "", false, fmt.Errorf("reading stage log: %w", err)
	}
	if len(ring) < c.stageLogCap {
		return string(ring), false, nil
	}
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
			return "", false, fmt.Errorf("reading stage log: %w", err)
		}
	}
	if !truncated {
		return string(ring), false, nil
	}
	// Rotate in place so the oldest byte comes first, without a second window.
	slices.Reverse(ring[:pos])
	slices.Reverse(ring[pos:])
	slices.Reverse(ring)
	if i := bytes.IndexByte(ring, '\n'); i >= 0 {
		ring = ring[i+1:]
	}
	return string(ring), true, nil
}

// openStageLog opens a pipeline node's log. It prefers the PGV endpoint
// (`/stages/log?nodeId=...`, which also serves step IDs) and falls back to
// Blue Ocean on 404. The legacy step-aggregation fallback remains for Blue
// Ocean parallel containers that return 500 on node log.
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
			return nil, fmt.Errorf("blue ocean plugin required for stage logs")
		}
		var se *jenkins.ServerError
		if errors.As(err, &se) {
			// Fallback: aggregate step-level logs when node log returns 500
			text, err := c.getStageLogViaSteps(segments, number, nodeID)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(strings.NewReader(text)), nil
		}
		return nil, fmt.Errorf("getting stage log: %w", err)
	}
	return resp.Body, nil
}

// getStageLogViaSteps fetches logs by aggregating individual step logs.
// Used as fallback when Blue Ocean's node-level /log/ returns 500.
func (c *Client) getStageLogViaSteps(blueSegments string, number int, nodeID string) (string, error) {
	stepsPath := fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/%s/steps/?limit=1000", blueSegments, number, nodeID)
	resp, err := c.Get(stepsPath, nil)
	if err != nil {
		return "", fmt.Errorf("getting stage steps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var steps []struct {
		ID   string `json:"id"`
		Name string `json:"displayName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steps); err != nil {
		return "", fmt.Errorf("decoding stage steps: %w", err)
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("no steps found for stage")
	}

	// Cap steps to avoid excessive requests for large stages.
	const maxSteps = 30
	if len(steps) > maxSteps {
		steps = steps[len(steps)-maxSteps:]
	}

	// Fetch step logs concurrently.
	logs := make([][]byte, len(steps))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, step := range steps {
		wg.Add(1)
		go func(i int, stepID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			logPath := fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/%s/steps/%s/log/", blueSegments, number, nodeID, stepID)
			logResp, err := c.Get(logPath, nil)
			if err != nil {
				return
			}
			const maxStep = 5 << 20 // 5 MB per step
			data, _ := io.ReadAll(io.LimitReader(logResp.Body, maxStep))
			_ = logResp.Body.Close()
			logs[i] = data
		}(i, step.ID)
	}
	wg.Wait()

	var buf strings.Builder
	for _, data := range logs {
		if len(data) > 0 {
			buf.Write(data)
			if data[len(data)-1] != '\n' {
				buf.WriteByte('\n')
			}
		}
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("no log content from stage steps")
	}
	return buf.String(), nil
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
