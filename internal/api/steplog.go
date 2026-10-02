package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ErrStepLogsUnavailable means a stage cannot be read step by step: no source
// lists its steps, or the server lacks core's per-node log route.
var ErrStepLogsUnavailable = errors.New("per-step stage logs unavailable")

// stepLogsUnavailable is ErrStepLogsUnavailable with the error behind it.
type stepLogsUnavailable struct{ cause error }

func (e *stepLogsUnavailable) Error() string {
	return ErrStepLogsUnavailable.Error() + ": " + e.cause.Error()
}
func (e *stepLogsUnavailable) Is(target error) bool { return target == ErrStepLogsUnavailable }
func (e *stepLogsUnavailable) Unwrap() error        { return e.cause }

// maxExceptionText bounds one exception text read. It is a message or a stack
// trace, never log output.
const maxExceptionText = 1 << 20

// maxStepListFailures is how many polls in a row a step listing may fail with
// a server error before the follow gives up on it.
const maxStepListFailures = 3

// StageStep is one step of a stage as a step listing reports it.
type StageStep struct {
	ID string
	// Active means the step may still write output.
	Active bool
	// Failed means the step ended in an error that PGV's stage log appends as
	// exception text.
	Failed bool
}

// ListStageSteps returns a stage's steps in the order the whole-stage log
// concatenates them. It prefers PGV's stages/steps and falls back to Blue Ocean
// on 404, like GetPipelineStages. fromPGV reports that PGV answered, since only
// PGV serves the exception text its stage log appends. When no source serves
// a listing the error is ErrStepLogsUnavailable.
func (c *Client) ListStageSteps(ctx context.Context, jobPath string, number int, nodeID string) (steps []StageStep, fromPGV bool, err error) {
	if c.pipelineSource != PipelineSourceBlueOcean {
		steps, err := c.listStageStepsPGV(ctx, jobPath, number, nodeID)
		if err == nil {
			return steps, true, nil
		}
		var nfe *jenkins.NotFoundError
		if !errors.As(err, &nfe) || c.pipelineSource == PipelineSourcePGV {
			return nil, false, stepListErr(err)
		}
	}
	if c.pipelineSource == PipelineSourcePGV {
		return nil, false, &stepLogsUnavailable{errors.New("PGV endpoint unavailable and fallback disabled")}
	}
	blue, err := c.listBlueSteps(ctx, blueStepsPrefix(normalizeBluePath(jobPath), number, nodeID))
	if err != nil {
		return nil, false, stepListErr(err)
	}
	steps = make([]StageStep, len(blue))
	for i, s := range blue {
		active := s.State == "RUNNING" || s.State == "QUEUED" || s.State == "PAUSED"
		steps[i] = StageStep{ID: s.ID, Active: active}
	}
	return steps, false, nil
}

func stepListErr(err error) error {
	var nfe *jenkins.NotFoundError
	if errors.As(err, &nfe) {
		return &stepLogsUnavailable{err}
	}
	return err
}

func (c *Client) listStageStepsPGV(ctx context.Context, jobPath string, number int, nodeID string) ([]StageStep, error) {
	path := fmt.Sprintf("%s/%d/stages/steps", NormalizeJobPath(jobPath), number)
	resp, err := c.getContext(ctx, path, url.Values{"nodeId": {nodeID}}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var pgv jenkins.PGVResponse
	if err := json.NewDecoder(resp.Body).Decode(&pgv); err != nil {
		return nil, fmt.Errorf("decoding PGV steps: %w", err)
	}
	if pgv.Status != "ok" {
		return nil, fmt.Errorf("PGV status %q", pgv.Status)
	}
	steps := make([]StageStep, len(pgv.Data.Steps))
	for i, s := range pgv.Data.Steps {
		switch jenkins.MapPGVState(s.State) {
		case "IN_PROGRESS", "PAUSED_PENDING_INPUT", "QUEUED":
			steps[i] = StageStep{ID: s.ID, Active: true}
		case "FAILURE", "ABORTED":
			steps[i] = StageStep{ID: s.ID, Failed: true}
		default:
			steps[i] = StageStep{ID: s.ID}
		}
	}
	return steps, nil
}

// stepLog reads a pipeline step's log through core's progressiveText. A step
// that has written nothing has no log action, so its route 404s.
func (c *Client) stepLog(jobPath string, number int, stepID string) *ProgressiveLog {
	path := fmt.Sprintf("%s/%d/execution/node/%s/log/logText/progressiveText", NormalizeJobPath(jobPath), number, url.PathEscape(stepID))
	l := c.NewProgressiveLog(path, 0)
	l.explain = func(err error) error {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return err
		}
		return fmt.Errorf("reading step %s log: %w", stepID, err)
	}
	return l
}

// GetStepExceptionText returns the error text PGV's stage log appends after a
// failed step: the message of a build failure, or a stack trace. It is "" when
// the step has none or PGV does not serve the route.
func (c *Client) GetStepExceptionText(ctx context.Context, jobPath string, number int, stepID string) (string, error) {
	path := fmt.Sprintf("%s/%d/stages/exceptionText", NormalizeJobPath(jobPath), number)
	resp, err := c.getContext(ctx, path, url.Values{"nodeId": {stepID}}, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return "", nil
		}
		return "", fmt.Errorf("getting step %s exception text: %w", stepID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxExceptionText))
	if err != nil {
		return "", fmt.Errorf("reading step %s exception text: %w", stepID, err)
	}
	return string(text), nil
}

// flowNodeExists reports whether core serves a flow node's page. It tells a
// step that has no log, whose log route 404s, from a server where the whole
// execution/node route is missing or blocked.
func (c *Client) flowNodeExists(ctx context.Context, jobPath string, number int, id string) (bool, error) {
	path := fmt.Sprintf("%s/%d/execution/node/%s/", NormalizeJobPath(jobPath), number, url.PathEscape(id))
	resp, err := c.getContext(ctx, path, nil, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return false, nil
		}
		return false, err
	}
	// Only the status matters; closing aborts the page download.
	_ = resp.Body.Close()
	return true, nil
}

// StageStepFollower follows a stage's log step by step. Neither whole-stage
// log endpoint takes an offset, but core serves each step's log from one, so
// every poll downloads only what the steps wrote since the last. See
// ProgressiveLog for how it finds where the last poll stopped.
//
// Steps print in listing order, and a step prints only once every earlier step
// has finished and been drained, which is the order the whole-stage log
// concatenates them in. A stage's listing holds only its own steps, which run
// one after another: steps of parallel branches belong to the branch nodes.
type StageStepFollower struct {
	c       *Client
	jobPath string
	number  int
	nodeID  string
	logs    map[string]*ProgressiveLog
	done    map[string]bool
	wrote   bool
	// routeOK is set once the per-node route is known to answer, so a 404
	// on a step log means only that the step has no log yet.
	routeOK      bool
	listFailures int
}

func (c *Client) NewStageStepFollower(jobPath string, number int, nodeID string) *StageStepFollower {
	return &StageStepFollower{
		c: c, jobPath: jobPath, number: number, nodeID: nodeID,
		logs: map[string]*ProgressiveLog{},
		done: map[string]bool{},
	}
}

// Poll writes the stage's new output to w. With final set the stage has ended:
// every step not yet done is read to its current end once, even one the
// listing still reports as running.
//
// ErrStepLogsUnavailable comes back only while nothing has been written, so
// the caller can switch to the whole-stage log without repeating output.
func (f *StageStepFollower) Poll(ctx context.Context, w io.Writer, final bool) error {
	// Marks output as soon as any byte is written, even by a read that then
	// fails, so a fallback can never print it again.
	w = &markingWriter{w: w, wrote: &f.wrote}
	steps, fromPGV, err := f.listSteps(ctx, final)
	if err != nil || steps == nil {
		return err
	}
	for _, s := range steps {
		if f.done[s.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		log := f.logs[s.ID]
		if log == nil {
			log = f.c.stepLog(f.jobPath, f.number, s.ID)
			f.logs[s.ID] = log
		}
		more, err := log.Read(ctx, w)
		var nfe *jenkins.NotFoundError
		switch {
		case err == nil:
			f.routeOK = true
			// A stalled step shows the rest once it completes, which every
			// server reports correctly.
			if final && log.Stalled() {
				return fmt.Errorf("step %s of stage %s has not closed its log, and the server does not report where its running text stops", s.ID, f.nodeID)
			}
		case errors.As(err, &nfe):
			if !f.routeOK {
				ok, err := f.c.flowNodeExists(ctx, f.jobPath, f.number, s.ID)
				if err != nil {
					return f.giveUp(ctx, err)
				}
				if !ok {
					return f.giveUp(ctx, fmt.Errorf("flow node %s not served", s.ID))
				}
				f.routeOK = true
			}
		default:
			return f.giveUp(ctx, err)
		}
		if !final && (s.Active || more) {
			return nil
		}
		f.done[s.ID] = true
		if s.Failed && fromPGV {
			if err := f.writeException(ctx, w, s.ID); err != nil {
				return f.giveUp(ctx, err)
			}
		}
	}
	return nil
}

// listSteps lists the stage's steps. A server error is retried on the next
// poll, maxStepListFailures times in a row, before the follow gives up; a
// final read retries at once. nil steps with a nil error mean "skip this
// poll".
func (f *StageStepFollower) listSteps(ctx context.Context, final bool) ([]StageStep, bool, error) {
	for {
		steps, fromPGV, err := f.c.ListStageSteps(ctx, f.jobPath, f.number, f.nodeID)
		var se *jenkins.ServerError
		switch {
		case err == nil:
			f.listFailures = 0
			if steps == nil {
				steps = []StageStep{}
			}
			return steps, fromPGV, nil
		case errors.As(err, &se) && ctx.Err() == nil:
			f.listFailures++
			if f.listFailures >= maxStepListFailures {
				return nil, false, f.giveUp(ctx, err)
			}
			if !final {
				return nil, false, nil
			}
		default:
			return nil, false, f.giveUp(ctx, err)
		}
	}
}

// writeException appends a failed step's exception text the way PGV's stage
// log does: after the step's log, ending in a newline.
func (f *StageStepFollower) writeException(ctx context.Context, w io.Writer, stepID string) error {
	text, err := f.c.GetStepExceptionText(ctx, f.jobPath, f.number, stepID)
	if err != nil || text == "" {
		return err
	}
	if text[len(text)-1] != '\n' {
		text += "\n"
	}
	_, err = io.WriteString(w, text)
	return err
}

// giveUp ends the step-by-step follow. While nothing is written, any failure
// other than cancellation becomes ErrStepLogsUnavailable, since the
// whole-stage log may still serve the stage. Past that the cause is returned,
// so its type still tells the caller what went wrong.
func (f *StageStepFollower) giveUp(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *stepLogsUnavailable
	isUnavailable := errors.As(err, &ue)
	switch {
	case !f.wrote && isUnavailable:
		return err
	case !f.wrote:
		return &stepLogsUnavailable{err}
	case isUnavailable:
		return fmt.Errorf("following stage %s step by step: %w", f.nodeID, ue.cause)
	}
	return err
}

// markingWriter sets *wrote once anything passes through it.
type markingWriter struct {
	w     io.Writer
	wrote *bool
}

func (m *markingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		*m.wrote = true
	}
	return m.w.Write(p)
}
