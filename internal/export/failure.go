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

// errorLine is an error line's place among all error lines and its text,
// empty for an "Also:" line.
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
	// build is the build's own "ERROR:", exception header and "Also:" lines,
	// oldest first.
	build []errorLine
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
		return
	}
	switch {
	case bytes.HasPrefix(line, []byte("Also:")):
		f.seq++
		f.addBuild(errorLine{seq: f.seq})
	case (bytes.Contains(line, []byte("Exception")) || bytes.Contains(line, []byte("Error"))) && exceptionHeader.Match(line):
		f.seq++
		f.addBuild(errorLine{f.seq, cleanErrorText(line)})
	}
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

// resolve picks the error that failed the build. That is the latest node
// error whose stage failed, unless the build wrote an error or exception
// after it: then the failure is the build's own, id is empty and msg is the
// first such line. A node error whose stage did not fail, such as a green
// step echoing "ERROR:", or that traces to no stage, does not count.
func (f *failureFinder) resolve(nodes []Node) (id, msg string) {
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
	after := false
	for _, e := range f.build {
		if e.seq <= best.seq {
			continue
		}
		after = true
		if e.msg != "" {
			return "", e.msg
		}
	}
	if after {
		return "", ""
	}
	return id, best.msg
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

// markFailure sets which node failed a failed or still running build: the
// stage f resolves when it resolves one, else the failed leaf stage that
// started last. f is nil when step logs were not read from the annotated
// console.
func (s *Snapshot) markFailure(f *failureFinder) {
	if s.Result != "FAILURE" && !s.Building {
		return
	}
	if f != nil {
		id, msg := f.resolve(s.Stages)
		s.FailureMessage = msg
		if msg != "" {
			if id != "" {
				s.FailureMessageSource = "node"
			} else {
				s.FailureMessageSource = "build"
			}
		}
		if id != "" {
			s.FailureNodeID = id
			return
		}
	}
	s.FailureNodeID = lastFailedLeaf(s.Stages)
}

func lastFailedLeaf(nodes []Node) string {
	var last *Node
	walk(nodes, func(n *Node) {
		if n.State == "FAILURE" && len(n.Children) == 0 && (last == nil || n.StartTimeMillis >= last.StartTimeMillis) {
			last = n
		}
	})
	if last == nil {
		return ""
	}
	return last.ID
}
