package export

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/ysmaoui/jkit/internal/output"
)

// maxFailureLineBytes bounds the error line kept. Detection needs only its
// start.
const maxFailureLineBytes = 4 << 10

// maxBuildErrors bounds the build-level error lines kept, newest kept.
const maxBuildErrors = 64

var (
	// timestampPrefix is Timestamper's hidden prefix, always in UTC.
	timestampPrefix = regexp.MustCompile(`^\[\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z\] ?`)
	// exceptionHeader is the first line of a Java stack trace, a qualified
	// class name. An uncaught exception other than AbortException ends the
	// build with one of these at build level and no "ERROR:" line.
	exceptionHeader = regexp.MustCompile(`^(?:[A-Za-z_$][\w$]*\.)+[A-Za-z_$][\w$]*(?:Exception|Error)(?::|$)`)
)

// errorLine is an error line's place among all error lines and its text.
type errorLine struct {
	seq int
	msg string
}

// failureFinder watches the split annotated console for error lines and the
// node that wrote each, and learns each flow node's parent from the
// pipeline-new-node spans, so that node can be traced up to its stage. The
// error is often written by a node no step listing holds, such as the
// closing "}" of a catchError body.
type failureFinder struct {
	parents map[string]string
	// held is, per node, the start of a line not yet ended; Jenkins' writes
	// and the download split lines anywhere.
	held map[string][]byte

	seq int
	// byNode is each node's last "ERROR:" line.
	byNode map[string]errorLine
	// build is the build's own "ERROR:" and exception header lines, oldest
	// first.
	build []errorLine
	// wantDetail is set after a build-level exception header that ends in
	// ":", as Groovy's "startup failed:", whose detail is the next line.
	wantDetail bool
	// branchFails is, oldest first, the place of each "Failed in branch"
	// line and the node that wrote it: a parallel branch's closing node,
	// when the branch threw.
	branchFails []branchFail
}

type branchFail struct {
	seq  int
	node string
}

func newFailureFinder() *failureFinder {
	return &failureFinder{parents: map[string]string{}, held: map[string][]byte{}, byNode: map[string]errorLine{}}
}

// newNode records a flow node. A block's end node hangs off its start node,
// any other off the node enclosing it.
func (f *failureFinder) newNode(id, start, enclosing string) {
	parent := enclosing
	if start != "" && start != id {
		parent = start
	}
	if id != "" && parent != "" && parent != id {
		f.parents[id] = parent
	}
}

// text takes a piece of node's split text.
func (f *failureFinder) text(node string, b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		seg := b
		if i >= 0 {
			seg = b[:i]
		}
		held, cont := f.held[node]
		if cont {
			seg = append(held, seg[:min(len(seg), maxFailureLineBytes-len(held))]...)
		}
		if i < 0 {
			f.held[node] = append([]byte(nil), seg[:min(len(seg), maxFailureLineBytes)]...)
			return
		}
		delete(f.held, node)
		f.line(node, seg)
		b = b[i+1:]
	}
}

func (f *failureFinder) line(node string, line []byte) {
	if len(line) > 0 && line[0] == '[' {
		if m := timestampPrefix.Find(line); m != nil {
			line = line[len(m):]
		}
	}
	line = bytes.TrimRight(bytes.TrimLeft(line, " \t"), "\r")
	if node == "" && len(line) > 0 && f.wantDetail {
		f.wantDetail = false
		if !bytes.HasPrefix(line, []byte("at ")) && !bytes.HasPrefix(line, []byte("ERROR:")) && !isExceptionHeader(line) {
			last := &f.build[len(f.build)-1]
			last.msg += " " + cleanErrorText(line)
			return
		}
	}
	if rest, ok := bytes.CutPrefix(line, []byte("ERROR:")); ok {
		f.seq++
		e := errorLine{f.seq, cleanErrorText(rest)}
		if node == "" {
			f.addBuild(e)
		} else {
			f.byNode[node] = e
		}
		return
	}
	if node != "" {
		if bytes.HasPrefix(line, []byte("Failed in branch ")) {
			f.seq++
			f.branchFails = append(f.branchFails, branchFail{f.seq, node})
		}
		return
	}
	if isExceptionHeader(line) {
		f.seq++
		msg := cleanErrorText(line)
		f.addBuild(errorLine{f.seq, msg})
		f.wantDetail = strings.HasSuffix(msg, ":")
	}
}

func isExceptionHeader(line []byte) bool {
	return (bytes.Contains(line, []byte("Exception")) || bytes.Contains(line, []byte("Error"))) && exceptionHeader.Match(line)
}

func (f *failureFinder) addBuild(e errorLine) {
	if len(f.build) == maxBuildErrors {
		f.build = f.build[1:]
	}
	f.build = append(f.build, e)
}

func cleanErrorText(b []byte) string {
	return strings.TrimSpace(output.StripControl(output.SanitizeLog(string(b[:min(len(b), maxFailureLineBytes)]))))
}

// resolve picks the errors that failed the build. id and msg are the latest
// node error whose stage failed; one whose stage did not fail, such as a
// green step echoing "ERROR:", or that traces to no stage, does not count.
// build is the first of the build's own error or exception lines written
// after it. Both are kept: when code after a catchError stage throws, such
// as notification code, the stage error is the actionable one. But when the
// build threw from another stage, that stage is the answer, with no message
// of its own: an uncaught failure writes its error at build level, not on a
// node. build is dropped when it repeats msg, as when a stage echoes its
// error and rethrows.
func (f *failureFinder) resolve(nodes []Node) (id, msg, build string) {
	byID := map[string]*Node{}
	walk(nodes, func(n *Node) { byID[n.ID] = n })
	var best errorLine
	for node, e := range f.byNode {
		if e.seq <= best.seq {
			continue
		}
		if n := byID[f.trace(node, byID)]; n != nil && n.State == "FAILURE" {
			best, id = e, n.ID
		}
	}
	msg = best.msg
	for _, e := range f.build {
		if e.seq > best.seq && e.msg != "" {
			build = e.msg
			break
		}
	}
	if build != "" && id != "" {
		if later := f.laterFailure(nodes, byID, byID[id], best.seq); later != "" {
			id, msg = later, ""
		}
	}
	if build == msg {
		build = ""
	}
	return id, msg, build
}

// laterFailure is the stage that threw after stage wrote its error at seq,
// "" for stage itself. Branches run at once, so start times do not order
// their failures; a branch that threw writes "Failed in branch" after the
// error instead. A sibling of a branch holding stage may write it before
// the error, and still fails the parallel stage is in; any other branch's
// earlier line belongs to a parallel that did not fail the build. A branch
// counts only when it and every enclosing node failed: a failure that
// reached the build leaves its whole chain failed, while a parallel a
// try/catch swallowed leaves its stage green. The first counted branch
// names the stage: an enclosing branch writes its own line after an inner
// one, and a branch failFast aborted is not FAILURE. When that branch holds
// stage, the stage after stage in the branch threw, as in a sequential run.
// With no counted line the run was sequential, and the failed leaf that
// started last threw, if it started after stage and lies outside it.
func (f *failureFinder) laterFailure(nodes []Node, byID map[string]*Node, stage *Node, seq int) string {
	parentOf := map[string]*Node{}
	walk(nodes, func(n *Node) {
		for i := range n.Children {
			parentOf[n.Children[i].ID] = n
		}
	})
	// failedChain is whether n and every node enclosing it ended failed. An
	// enclosing parallel failFast stopped ends ABORTED.
	failedChain := func(n *Node) bool {
		for p := parentOf[n.ID]; p != nil; p = parentOf[p.ID] {
			if p.State != "FAILURE" && p.State != "ABORTED" {
				return false
			}
		}
		return n.State == "FAILURE"
	}
	branched := false
	for _, e := range f.branchFails {
		b := byID[f.trace(e.node, byID)]
		if b == nil {
			if e.seq > seq {
				branched = true
			}
			continue
		}
		if e.seq <= seq && (parentOf[b.ID] == nil || !contains(parentOf[b.ID], stage)) {
			continue
		}
		if !failedChain(b) {
			continue
		}
		branched = true
		if contains(b, stage) {
			if leaf := laterLeaf([]Node{*b}, stage); leaf != nil {
				return leaf.ID
			}
			continue
		}
		if leaf := lastFailedLeaf([]Node{*b}); leaf != nil {
			return leaf.ID
		}
		return b.ID
	}
	if branched {
		return ""
	}
	if leaf := laterLeaf(nodes, stage); leaf != nil {
		return leaf.ID
	}
	return ""
}

// laterLeaf is the failed leaf under nodes that started last, if it started
// after stage and lies outside it.
func laterLeaf(nodes []Node, stage *Node) *Node {
	if leaf := lastFailedLeaf(nodes); leaf != nil && leaf.StartTimeMillis > stage.StartTimeMillis && !contains(stage, leaf) {
		return leaf
	}
	return nil
}

// contains reports whether n is root or lies under it.
func contains(root, n *Node) bool {
	found := false
	walk([]Node{*root}, func(m *Node) { found = found || m.ID == n.ID })
	return found
}

// trace is the first of node and its ancestors that is in the tree, "" for
// none.
func (f *failureFinder) trace(node string, byID map[string]*Node) string {
	// Bounded so a malformed parent loop cannot spin.
	for range len(f.parents) + 1 {
		if byID[node] != nil {
			return node
		}
		p, ok := f.parents[node]
		if !ok {
			break
		}
		node = p
	}
	return ""
}

// markFailure sets which node failed a failed or still running build and
// the error lines naming why: the stage f resolves when it resolves one,
// else the failed leaf stage that started last, with no stage message. f is
// nil when step logs were not read from the annotated console, and when the
// run was not complete, since the console text it saw may stop short.
func (s *Snapshot) markFailure(f *failureFinder) {
	if s.Result != "FAILURE" && !s.Building {
		return
	}
	if f != nil {
		id, msg, build := f.resolve(s.Stages)
		s.BuildErrorMessage = build
		if id != "" {
			s.FailureNodeID, s.FailureMessage = id, msg
			return
		}
	}
	if leaf := lastFailedLeaf(s.Stages); leaf != nil {
		s.FailureNodeID = leaf.ID
	}
}

func lastFailedLeaf(nodes []Node) *Node {
	var last *Node
	walk(nodes, func(n *Node) {
		if n.State == "FAILURE" && len(n.Children) == 0 && (last == nil || n.StartTimeMillis >= last.StartTimeMillis) {
			last = n
		}
	})
	return last
}
