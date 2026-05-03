package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/ysmaoui/jk/internal/jenkins"
)

// GetPipelineStages returns the flat stage list for a build. It prefers the
// Pipeline Graph View plugin (`/stages/tree`, v803+) and falls back to Blue
// Ocean (`/blue/rest/.../nodes/`) on 404 or when the client is pinned to
// Blue Ocean via JK_PIPELINE_SOURCE / WithPipelineSource.
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
	return jenkins.FlattenPGVTree(pgv.Data.Stages), nil
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

// GetStageLog returns the console log for a pipeline node. It prefers the PGV
// endpoint (`/stages/log?nodeId=...`, which also serves step IDs) and falls
// back to Blue Ocean on 404. The legacy step-aggregation fallback remains for
// Blue Ocean parallel containers that return 500 on node log.
func (c *Client) GetStageLog(jobPath string, number int, nodeID string) (string, error) {
	if c.pipelineSource != PipelineSourceBlueOcean {
		log, err := c.getStageLogPGV(jobPath, number, nodeID)
		if err == nil {
			return log, nil
		}
		var nfe *jenkins.NotFoundError
		if !errors.As(err, &nfe) || c.pipelineSource == PipelineSourcePGV {
			return "", err
		}
		// 404 → fall through to Blue Ocean
	}
	if c.pipelineSource == PipelineSourcePGV {
		return "", fmt.Errorf("PGV endpoint unavailable and fallback disabled")
	}
	return c.getStageLogBlueOcean(jobPath, number, nodeID)
}

func (c *Client) getStageLogPGV(jobPath string, number int, nodeID string) (string, error) {
	path := fmt.Sprintf("%s/%d/stages/log", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"nodeId": {nodeID}})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	const maxLog = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLog))
	if err != nil {
		return "", fmt.Errorf("reading PGV stage log: %w", err)
	}
	return string(data), nil
}

func (c *Client) getStageLogBlueOcean(jobPath string, number int, nodeID string) (string, error) {
	segments := normalizeBluePath(jobPath)
	path := fmt.Sprintf("/blue/rest/organizations/jenkins/pipelines/%s/runs/%d/nodes/%s/log/", segments, number, nodeID)

	resp, err := c.Get(path, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return "", fmt.Errorf("blue ocean plugin required for stage logs")
		}
		var se *jenkins.ServerError
		if errors.As(err, &se) {
			// Fallback: aggregate step-level logs when node log returns 500
			return c.getStageLogViaSteps(segments, number, nodeID)
		}
		return "", fmt.Errorf("getting stage log: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxLog = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLog))
	if err != nil {
		return "", fmt.Errorf("reading stage log: %w", err)
	}
	return string(data), nil
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
