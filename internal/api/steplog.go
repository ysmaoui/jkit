package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ErrStepLogsUnavailable means a stage cannot be read step by step: no source
// lists its steps, the server lacks core's per-node log route, or it cannot
// say where a running step's log stops.
var ErrStepLogsUnavailable = errors.New("per-step stage logs unavailable")

// stepLogsUnavailable is ErrStepLogsUnavailable with the error behind it.
type stepLogsUnavailable struct{ cause error }

func (e *stepLogsUnavailable) Error() string {
	return ErrStepLogsUnavailable.Error() + ": " + e.cause.Error()
}
func (e *stepLogsUnavailable) Is(target error) bool { return target == ErrStepLogsUnavailable }
func (e *stepLogsUnavailable) Unwrap() error        { return e.cause }

// errStepOffsetUnknown means a plain progressiveText answer for a running step
// came from a server whose X-Text-Size may run past the body it sends.
var errStepOffsetUnknown = errors.New("server does not report where a running step's log text stops")

// errStepLogRestarted means the server answered from an earlier offset than
// asked, which Stapler does when the stored log is shorter than the offset:
// it resends the log from 0.
var errStepLogRestarted = errors.New("server restarted the step log from an earlier offset")

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

// CopyStepLogFrom copies a pipeline step's log from byte offset start to w
// through core's progressiveText and returns the offset to ask for next. more
// reports that the step may still write. A step that has written nothing has
// no log action, so its route 404s. Cancelling ctx aborts the request at any
// point.
//
// Where the next offset comes from depends on the Stapler version behind the
// server, so the request asks for the multipart streaming answer (Stapler
// 2050, Jenkins 2.534) and reads a plain one by what the server's version
// makes of X-Text-Size:
//
//   - multipart: the text part runs up to the last complete line, with console
//     notes stripped, and the meta part's "end" is the stored offset where it
//     stops.
//   - plain, step complete: every Stapler sends the rest of the log and sets
//     X-Text-Size to its end.
//   - plain, step running, Jenkins up to 2.508 (Stapler before 1979):
//     X-Text-Size is counted from what was sent, up to the last complete line.
//   - plain, step running, any later Jenkins: X-Text-Size is the stored length.
//     Stapler 2029 (#703) and later stop the body at the last complete line and
//     after 10000 lines, so the gap would be lost; 1979 to 2028 could send a
//     body running past X-Text-Size. errStepOffsetUnknown comes back before the
//     body is read.
//
// A server answering from an earlier offset than start, as Stapler does once
// the stored log is shorter than start, gives errStepLogRestarted. A plain
// answer is caught before its body is read. A streaming one shows it only in
// the meta part after the text, so the resent text may already be written.
// Step logs only grow, so neither is expected.
//
// Plain bodies go through Stapler's LineEndNormalizingWriter, which turns a
// lone LF into CRLF; lfWriter turns them back.
func (c *Client) CopyStepLogFrom(ctx context.Context, jobPath string, number int, stepID string, start int64, w io.Writer) (next int64, more bool, err error) {
	path := fmt.Sprintf("%s/%d/execution/node/%s/log/logText/progressiveText", NormalizeJobPath(jobPath), number, url.PathEscape(stepID))
	resp, err := c.getContext(ctx, path, url.Values{"start": {strconv.FormatInt(start, 10)}},
		http.Header{"Accept": {"multipart/form-data"}})
	if err != nil {
		return start, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	mediaType, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		next, more, err = copyStreamingStepLog(resp.Body, params["boundary"], start, w)
	} else {
		next, more, err = copyPlainStepLog(resp, start, w)
	}
	if err != nil {
		if ctx.Err() != nil {
			return start, false, ctx.Err()
		}
		if errors.Is(err, errStepOffsetUnknown) {
			return start, more, err
		}
		return start, false, fmt.Errorf("reading step %s log: %w", stepID, err)
	}
	return next, more, nil
}

func copyStreamingStepLog(body io.Reader, boundary string, start int64, w io.Writer) (next int64, more bool, err error) {
	mr := multipart.NewReader(body, boundary)
	text, err := mr.NextPart()
	if err != nil {
		return 0, false, err
	}
	if text.FormName() != "text" {
		return 0, false, fmt.Errorf("streaming answer starts with part %q, not text", text.FormName())
	}
	if _, err := io.Copy(w, text); err != nil {
		return 0, false, err
	}
	meta, err := mr.NextPart()
	if err != nil {
		return 0, false, fmt.Errorf("streaming answer has no meta part: %w", err)
	}
	var m struct {
		Completed bool   `json:"completed"`
		Start     *int64 `json:"start"`
		End       *int64 `json:"end"`
	}
	if err := json.NewDecoder(meta).Decode(&m); err != nil {
		return 0, false, fmt.Errorf("decoding streaming meta: %w", err)
	}
	if m.End == nil {
		return 0, false, errors.New("streaming meta has no end offset")
	}
	if m.Start != nil && *m.Start != start {
		return 0, false, fmt.Errorf("%w: asked for %d, got %d", errStepLogRestarted, start, *m.Start)
	}
	return *m.End, !m.Completed, nil
}

func copyPlainStepLog(resp *http.Response, start int64, w io.Writer) (next int64, more bool, err error) {
	size, err := strconv.ParseInt(resp.Header.Get("X-Text-Size"), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("no usable X-Text-Size: %w", err)
	}
	if size < start {
		return 0, false, fmt.Errorf("%w: asked for %d, log ends at %d", errStepLogRestarted, start, size)
	}
	more = resp.Header.Get("X-More-Data") == "true"
	if more && size > start && !countsTextSize(resp.Header.Get("X-Jenkins")) {
		return 0, true, errStepOffsetUnknown
	}
	lf := &lfWriter{w: w}
	if _, err := io.Copy(lf, resp.Body); err != nil {
		return 0, false, err
	}
	if err := lf.Flush(); err != nil {
		return 0, false, err
	}
	return size, more, nil
}

// countsTextSize reports whether a Jenkins version answers a plain
// progressiveText request for an unfinished log with the offset it read up to.
// Stapler 1979 (#657), first in Jenkins 2.509, sends the stored length instead.
// An unknown version is not trusted.
func countsTextSize(version string) bool {
	major, rest, ok := strings.Cut(version, ".")
	minor, _, _ := strings.Cut(rest, ".")
	maj, err1 := strconv.Atoi(major)
	mnr, err2 := strconv.Atoi(minor)
	if !ok || err1 != nil || err2 != nil {
		return false
	}
	return maj < 2 || maj == 2 && mnr < 509
}

// lfWriter undoes Stapler's LF-to-CRLF rewrite of plain progressiveText, so
// step output matches the whole-stage log, which is sent as stored. It drops
// every CR before an LF, so a CRLF the step itself wrote comes out as LF. A CR
// ending one write is held until the next shows what follows.
type lfWriter struct {
	w         io.Writer
	pendingCR bool
}

func (l *lfWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	buf := make([]byte, 0, len(p)+1)
	if l.pendingCR && p[0] != '\n' {
		buf = append(buf, '\r')
	}
	l.pendingCR = false
	for i, b := range p {
		if b == '\r' {
			if i == len(p)-1 {
				l.pendingCR = true
				continue
			}
			if p[i+1] == '\n' {
				continue
			}
		}
		buf = append(buf, b)
	}
	if _, err := l.w.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes a held CR. The next response cannot pair it with an LF: the
// server rewrites an LF there as CRLF, which is dropped back to LF.
func (l *lfWriter) Flush() error {
	if !l.pendingCR {
		return nil
	}
	l.pendingCR = false
	_, err := l.w.Write([]byte{'\r'})
	return err
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
// every poll downloads only what the steps wrote since the last.
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
	offsets map[string]int64
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
		offsets: map[string]int64{},
		done:    map[string]bool{},
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
		start := f.offsets[s.ID]
		next, more, err := f.c.CopyStepLogFrom(ctx, f.jobPath, f.number, s.ID, start, w)
		var nfe *jenkins.NotFoundError
		switch {
		case err == nil:
			f.offsets[s.ID] = next
			f.routeOK = true
		case errors.Is(err, errStepOffsetUnknown):
			if !f.wrote {
				return &stepLogsUnavailable{err}
			}
			// Earlier steps are printed, so the whole-stage log cannot take
			// over. The step waits until it completes, which every server
			// reports correctly.
			if final {
				return fmt.Errorf("step %s of stage %s has not closed its log, and %w", s.ID, f.nodeID, err)
			}
			return nil
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
