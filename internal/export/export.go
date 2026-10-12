// Package export collects a pipeline run's stages, steps and logs into one
// Snapshot, the data an offline HTML report is rendered from.
package export

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
)

// Snapshot is a run as it stood when collected. Its JSON is what the HTML
// template reads. States are jenkins.MapPGVState values.
type Snapshot struct {
	Job              string `json:"job"`
	Number           int    `json:"number"`
	Result           string `json:"result"`
	Building         bool   `json:"building"`
	StartTimeMillis  int64  `json:"startTimeMillis"`
	DurationMillis   int64  `json:"durationMillis"`
	URL              string `json:"url"`
	ExportedAtMillis int64  `json:"exportedAtMillis"`
	// Complete is false when the run was still going at export, so stages,
	// steps and logs may stop short.
	Complete bool   `json:"complete"`
	Stages   []Node `json:"stages"`
	// Console is the whole build log. It is the only place for the output of
	// steps outside any stage and of block steps such as node, which the step
	// listing leaves out.
	Console Log `json:"console"`
	spools  []*spool
	// ConsoleError is why the console could not be read in full. Console then
	// holds what arrived, marked Partial. It sits here rather than on Log
	// because Step.Error is the same kind of note and Log is only text.
	ConsoleError string `json:"consoleError"`
	// FailureNodeID is the stage or branch that failed the build, empty when
	// the build did not fail or no stage did. A catchError stage marked
	// FAILURE is not it when a later error stopped the build.
	FailureNodeID string `json:"failureNodeId"`
	// FailureMessage is the "ERROR:" line or exception header that named
	// FailureNodeID, or the build's own when the build threw. It is read only
	// with the annotated console.
	FailureMessage string `json:"failureMessage"`
	// FailureMessageSource is "node" when FailureMessage came from
	// FailureNodeID's own output, "build" when the build threw and
	// FailureNodeID is only the fallback leaf, empty with no message.
	FailureMessageSource string `json:"failureMessageSource"`
}

// Node is a stage or parallel branch. A parallel wrapper is a STAGE whose
// children are PARALLEL nodes; containers usually have no steps of their own.
type Node struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	State string `json:"state"`
	// Synthetic marks nodes the pipeline did not declare, such as declarative
	// "Post Actions", and the UNLISTED stand-ins Collect adds.
	Synthetic       bool   `json:"synthetic"`
	Running         bool   `json:"running"`
	StartTimeMillis int64  `json:"startTimeMillis"`
	DurationMillis  int64  `json:"durationMillis"`
	Agent           string `json:"agent"`
	Children        []Node `json:"children"`
	Steps           []Step `json:"steps"`
}

// UnlistedType is the type of a top-level stand-in node for a stageId that
// steps carry but the stage tree does not hold. The tree is read after the
// steps, so this happens only when PGV leaves a node out.
const UnlistedType = "UNLISTED"

// Step is one step. Title is the step's display name, empty for sh and echo;
// Name is its argument, such as the sh script. Show Title, else Name.
type Step struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Title           string `json:"title"`
	State           string `json:"state"`
	Running         bool   `json:"running"`
	StartTimeMillis int64  `json:"startTimeMillis"`
	DurationMillis  int64  `json:"durationMillis"`
	Log             Log    `json:"log"`
	// Exception is the error PGV records on a failed or aborted step. The
	// error step writes no log; its message is only here.
	Exception string `json:"exception"`
	// Error is why this step's log or exception could not be read.
	Error string `json:"error"`
}

// Log is sanitized text, gzipped then base64-encoded, empty when the text is.
type Log struct {
	// GzipBase64 is empty on a Log from Collect, which keeps the text in the
	// Snapshot's spool file; Render writes it here in the JSON either way.
	GzipBase64 string `json:"gzipBase64"`
	// Bytes is the sanitized length before any cut.
	Bytes int64 `json:"bytes"`
	// CutBytes is how much a MaxLogBytes cap dropped from the middle, where a
	// marker line stands in for it.
	CutBytes int64 `json:"cutBytes"`
	// Partial means the log may stop short of what the source had written: a
	// read error cut it short, or, on Jenkins 2.509 to 2.533, a running
	// step's log is read only to the 10000-line cap.
	Partial bool `json:"partial"`

	// The text is n bytes at off in spool, when spool is set.
	spool  *spool
	off, n int64
}

// Options tunes Collect.
type Options struct {
	// MaxLogBytes caps each log, keeping half from the head and half from the
	// tail. 0 keeps everything.
	MaxLogBytes int64
	// Progress, when set, is called as each log's read starts, from one
	// goroutine and in step order: step i of total with the node holding it,
	// then the console with node and step nil.
	Progress func(i, total int, node *Node, step *Step)
	// SpoolDir is where the encoded logs wait for Render, os.TempDir when
	// empty. It needs about as much free space as the HTML file.
	SpoolDir string
	// Concurrency is how many step logs are read at once, 1 when below 1.
	// Each reader writes its own spool file.
	Concurrency int
	// LogSource picks where step logs come from.
	LogSource LogSource
	// Notice, when set, gets one line naming the log source read, or why the
	// annotated console was given up.
	Notice func(msg string)
}

// LogSource is where Collect reads step logs from.
type LogSource string

const (
	// LogSourceAuto is LogSourceConsole for a finished run, else
	// LogSourceSteps: a running run's annotated console can stop short, or
	// on newer Jenkins end mid-line.
	LogSourceAuto LogSource = ""
	// LogSourceConsole splits one annotated-console download into step
	// logs, and falls back to LogSourceSteps when it cannot.
	LogSourceConsole LogSource = "console"
	// LogSourceSteps reads each step's log on its own, Concurrency at once.
	LogSourceSteps LogSource = "steps"
)

// timeoutHint is added to a log read that timed out: one log is one HTTP
// response, and the client timeout covers reading its body.
const timeoutHint = "the whole log has to download within the HTTP timeout; raise it with --timeout (e.g. --timeout 5m)"

// Collect reads a build's metadata, PGV stage tree, steps, step logs and
// console. It works on a running build, giving what exists so far. The caller
// must Close the Snapshot.
func Collect(ctx context.Context, client *api.Client, jobPath string, number int, opts Options) (*Snapshot, error) {
	snap := &Snapshot{Job: jobPath, Number: number, ExportedAtMillis: time.Now().UnixMilli()}

	build, err := client.GetBuild(jobPath, number)
	if err != nil {
		return nil, err
	}
	snap.Result = build.Result
	snap.Building = build.Building
	snap.StartTimeMillis = build.Timestamp
	snap.DurationMillis = build.Duration
	snap.URL = build.URL

	// Steps before the tree: a stage that starts in between then shows up
	// in the tree without steps, rather than its steps without a stage.
	steps, runComplete, err := client.ListAllSteps(ctx, jobPath, number)
	if err != nil {
		return nil, fmt.Errorf("listing steps: %w", err)
	}
	tree, treeComplete, err := client.GetPGVTree(ctx, jobPath, number)
	if err != nil {
		return nil, fmt.Errorf("reading stage tree: %w", err)
	}
	snap.Complete = treeComplete && runComplete && !build.Building
	snap.Stages = buildTree(tree, steps)

	for range max(opts.Concurrency, 1) {
		sp, err := newSpool(opts.SpoolDir)
		if err != nil {
			_ = snap.Close()
			return nil, err
		}
		snap.spools = append(snap.spools, sp)
	}
	if err := collectLogs(ctx, client, jobPath, number, opts, snap); err != nil {
		_ = snap.Close()
		return nil, err
	}
	return snap, nil
}

// Close removes the spool files that hold the logs. The Snapshot cannot be
// rendered after.
func (s *Snapshot) Close() error {
	var err error
	for _, sp := range s.spools {
		err = cmp.Or(sp.close(), err)
	}
	s.spools = nil
	return err
}

func collectLogs(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, snap *Snapshot) error {
	src := opts.LogSource
	if src == LogSourceAuto {
		src = LogSourceSteps
		if snap.Complete {
			src = LogSourceConsole
		}
	}
	if src == LogSourceConsole {
		return collectFromConsole(ctx, client, jobPath, number, opts, snap)
	}
	if err := fillStepLogs(ctx, client, jobPath, number, opts, snap.spools, snap.Stages); err != nil {
		return err
	}
	// The plain console has no node attribution to trace an error line by.
	snap.markFailure(nil)
	if opts.Progress != nil {
		opts.Progress(0, 0, nil, nil)
	}
	return readConsole(ctx, client, jobPath, number, opts.MaxLogBytes, snap.spools[0], snap)
}

// readConsole reads the plain console into snap.Console on sp. Only an error
// that stops the export is returned.
func readConsole(ctx context.Context, client *api.Client, jobPath string, number int, maxBytes int64, sp *spool, snap *Snapshot) error {
	var err error
	snap.Console, err = encodeLog(sp, maxBytes, func(w io.Writer) error {
		body, err := client.OpenConsoleText(jobPath, number)
		if err != nil {
			return err
		}
		// OpenConsoleText takes no context, so a cancel closes the body.
		stop := context.AfterFunc(ctx, func() { _ = body.Close() })
		defer stop()
		defer func() { _ = body.Close() }()
		if _, err := io.Copy(w, body); err != nil {
			return fmt.Errorf("reading console: %w", err)
		}
		return nil
	})
	if err != nil {
		err = withTimeoutHint(err)
		if stopsExport(ctx, err) {
			return err
		}
		snap.Console.Partial = true
		snap.ConsoleError = err.Error()
	}
	return nil
}

// buildTree converts the PGV tree, drops its start node and hangs each step
// on the node its stageId names, in start order.
func buildTree(tree []jenkins.PGVStage, steps []jenkins.PGVStep) []Node {
	nodes := convertNodes(tree)

	known := map[string]bool{}
	walk(nodes, func(n *Node) { known[n.ID] = true })
	for _, s := range steps {
		if !known[s.StageID] {
			known[s.StageID] = true
			nodes = append(nodes, Node{ID: s.StageID, Name: "Node " + s.StageID, Type: UnlistedType, Synthetic: true, Children: []Node{}, Steps: []Step{}})
		}
	}

	byID := map[string]*Node{}
	walk(nodes, func(n *Node) { byID[n.ID] = n })
	for _, s := range steps {
		n := byID[s.StageID]
		state := jenkins.MapPGVState(s.State)
		n.Steps = append(n.Steps, Step{
			ID:              s.ID,
			Name:            s.Name,
			Title:           s.Title,
			State:           state,
			Running:         running(state),
			StartTimeMillis: s.StartTimeMillis,
			DurationMillis:  s.TotalDurationMillis,
		})
	}
	walk(nodes, func(n *Node) {
		slices.SortStableFunc(n.Steps, func(a, b Step) int {
			return cmp.Or(cmp.Compare(a.StartTimeMillis, b.StartTimeMillis), compareIDs(a.ID, b.ID))
		})
		if n.Type == UnlistedType {
			n.Running = slices.ContainsFunc(n.Steps, func(s Step) bool { return s.Running })
		}
	})
	return nodes
}

func convertNodes(stages []jenkins.PGVStage) []Node {
	out := []Node{}
	for _, s := range stages {
		if s.Type == jenkins.PGVPipelineStart {
			continue
		}
		state := jenkins.MapPGVState(s.State)
		out = append(out, Node{
			ID:              s.ID,
			Name:            s.Name,
			Type:            s.Type,
			State:           state,
			Synthetic:       s.Synthetic,
			Running:         running(state),
			StartTimeMillis: s.StartTimeMillis,
			DurationMillis:  s.TotalDurationMillis,
			Agent:           s.Agent,
			Children:        convertNodes(s.Children),
			Steps:           []Step{},
		})
	}
	return out
}

// walk visits every node depth-first. The pointers stay valid only until a
// slice they point into is appended to.
func walk(nodes []Node, fn func(*Node)) {
	for i := range nodes {
		fn(&nodes[i])
		walk(nodes[i].Children, fn)
	}
}

func running(state string) bool {
	switch state {
	case "IN_PROGRESS", "PAUSED_PENDING_INPUT", "QUEUED":
		return true
	}
	return false
}

// compareIDs orders flow node ids numerically, as PGV allocates them.
func compareIDs(a, b string) int {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	if errA == nil && errB == nil {
		return cmp.Compare(x, y)
	}
	return cmp.Compare(a, b)
}

// EachStep calls fn for every step with the node holding it, depth-first.
func (s *Snapshot) EachStep(fn func(*Node, *Step)) {
	walk(s.Stages, func(n *Node) {
		for i := range n.Steps {
			fn(n, &n.Steps[i])
		}
	})
}

// fillStepLogs reads step logs with one worker per spool, each worker the
// only writer of its spool. Progress runs in step order once a worker has
// taken the step. The first fatal error cancels the reads still going.
func fillStepLogs(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, spools []*spool, nodes []Node) error {
	type job struct {
		node *Node
		step *Step
	}
	var jobs []job
	walk(nodes, func(n *Node) {
		for i := range n.Steps {
			jobs = append(jobs, job{n, &n.Steps[i]})
		}
	})

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu    sync.Mutex
		fatal error
		wg    sync.WaitGroup
	)
	queue := make(chan *Step)
	for _, sp := range spools {
		wg.Go(func() {
			for s := range queue {
				if err := fillStep(workCtx, client, jobPath, number, opts, sp, s); err != nil {
					mu.Lock()
					fatal = cmp.Or(fatal, err)
					mu.Unlock()
					cancel()
				}
			}
		})
	}
dispatch:
	for i, j := range jobs {
		if workCtx.Err() != nil {
			break
		}
		select {
		case queue <- j.step:
		case <-workCtx.Done():
			break dispatch
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(jobs), j.node, j.step)
		}
	}
	close(queue)
	wg.Wait()
	return cmp.Or(fatal, ctx.Err())
}

// fillStep reads one step's log and exception text. A failure that would
// repeat for every step is returned; any other is recorded on the step.
func fillStep(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, sp *spool, s *Step) error {
	var partial bool
	log, err := encodeLog(sp, opts.MaxLogBytes, func(w io.Writer) error {
		var err error
		_, partial, err = client.CopyStepLog(ctx, jobPath, number, s.ID, w)
		return err
	})
	log.Partial = partial
	s.Log = log
	if err != nil {
		if stopsExport(ctx, err) {
			return withTimeoutHint(err)
		}
		s.Error = withTimeoutHint(err).Error()
		s.Log.Partial = true
	}
	return fillException(ctx, client, jobPath, number, s)
}

// fillException reads the exception text of a failed or aborted step, the
// only states that carry one.
func fillException(ctx context.Context, client *api.Client, jobPath string, number int, s *Step) error {
	if s.State != "FAILURE" && s.State != "ABORTED" {
		return nil
	}
	text, err := client.StepExceptionText(ctx, jobPath, number, s.ID)
	if err != nil {
		err = withTimeoutHint(err)
		if stopsExport(ctx, err) {
			return err
		}
		if s.Error != "" {
			s.Error += "\n"
		}
		s.Error += err.Error()
	}
	s.Exception = text
	return nil
}

// stopsExport reports errors every later request would hit too. A timeout is
// not one, even when the client gave up on it as unreachable: a large log can
// be slow to start while the next one is not.
func stopsExport(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, errSpool) {
		return true
	}
	var pme *jenkins.PluginMissingError
	var ae *jenkins.AuthError
	var pe *jenkins.PermissionError
	var ue *jenkins.UnreachableError
	if errors.As(err, &ue) {
		return !isTimeout(err)
	}
	return errors.As(err, &pme) || errors.As(err, &ae) || errors.As(err, &pe)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func withTimeoutHint(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("%w\n%s", err, timeoutHint)
	}
	return err
}
