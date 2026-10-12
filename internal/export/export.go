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
	GzipBase64 string `json:"gzipBase64"`
	// Bytes is the sanitized length before any cut.
	Bytes int64 `json:"bytes"`
	// CutBytes is how much a MaxLogBytes cap dropped from the middle, where a
	// marker line stands in for it.
	CutBytes int64 `json:"cutBytes"`
	// Partial means the log may stop short of what the step had written: on
	// Jenkins 2.509 to 2.533 a running step's log is read only to the
	// 10000-line cap.
	Partial bool `json:"partial"`
}

// Options tunes Collect.
type Options struct {
	// MaxLogBytes caps each log, keeping half from the head and half from the
	// tail. 0 keeps everything.
	MaxLogBytes int64
}

// timeoutHint is added to a log read that timed out: one log is one HTTP
// response, and the client timeout covers reading its body.
const timeoutHint = "the whole log has to download within the HTTP timeout; raise it with --timeout (e.g. --timeout 5m)"

// Collect reads a build's metadata, PGV stage tree, steps, step logs and
// console. It works on a running build, giving what exists so far.
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

	if err := fillStepLogs(ctx, client, jobPath, number, opts, snap.Stages); err != nil {
		return nil, err
	}

	snap.Console, err = encodeLog(opts.MaxLogBytes, func(w io.Writer) error {
		body, err := client.OpenConsoleText(jobPath, number)
		if err != nil {
			return err
		}
		defer func() { _ = body.Close() }()
		if _, err := io.Copy(w, body); err != nil {
			return fmt.Errorf("reading console: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, withTimeoutHint(err)
	}
	return snap, nil
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

func fillStepLogs(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, nodes []Node) error {
	var fatal error
	walk(nodes, func(n *Node) {
		for i := range n.Steps {
			if fatal != nil {
				return
			}
			fatal = fillStep(ctx, client, jobPath, number, opts, &n.Steps[i])
		}
	})
	return fatal
}

// fillStep reads one step's log and exception text. A failure that would
// repeat for every step is returned; any other is recorded on the step.
func fillStep(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, s *Step) error {
	var partial bool
	log, err := encodeLog(opts.MaxLogBytes, func(w io.Writer) error {
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
	}

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
	if ctx.Err() != nil {
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
