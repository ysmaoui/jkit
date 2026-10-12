// Command exportcheck asserts what a `jkit export html` file of the seeded
// export-pipeline job holds. run.sh calls it:
//
//	exportcheck finished <export.html> <allSteps.json>
//	exportcheck live <snapshot.html>
//	exportcheck same <a.html> <b.html>
//
// finished checks an export of the ended run against PGV's step listing;
// live checks the snapshot the run took of itself from its Snapshot stage;
// same checks that two exports of one run, from either log source, hold the
// same steps, logs and console.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/ysmaoui/jkit/internal/export"
	"github.com/ysmaoui/jkit/internal/jenkins"
)

const (
	snapshotOpen = `<script type="application/json" id="snapshot">`
	longLines    = 20000
	usage        = "usage: exportcheck finished <html> <allSteps.json> | live <html> | same <html> <html>"
)

var exportedAt = regexp.MustCompile(`"exportedAtMillis":\d+`)

// stagesBeforeSnapshot have all ended when the Snapshot stage runs.
var stagesBeforeSnapshot = []string{"Build", "Long Log", "Flaky", "Fan Out", "Branch A", "Branch B", "B1", "B2"}

var (
	// Timestamper prefixes plain step-log lines with [ISO-8601 UTC]
	numbered  = regexp.MustCompile(`^(?:\[\d{4}-\d\d-\d\dT[\d:.]+Z\] )?\d+$`)
	remoteRef = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']?\s*https?:|url\(\s*["']?\s*https?:|@import\s+["']?https?:`)
)

type checker struct{ failures []string }

func (c *checker) check(ok bool, format string, args ...any) {
	if !ok {
		c.failures = append(c.failures, fmt.Sprintf(format, args...))
	}
}

func main() {
	if len(os.Args) < 3 {
		fatal(usage)
	}
	page, err := os.ReadFile(os.Args[2])
	if err != nil {
		fatal(err.Error())
	}
	snap, err := snapshotOf(page)
	if err != nil {
		fatal(err.Error())
	}

	c := &checker{}
	switch {
	case os.Args[1] == "finished" && len(os.Args) == 4:
		finished(c, page, snap, os.Args[3])
	case os.Args[1] == "live" && len(os.Args) == 3:
		live(c, snap)
	case os.Args[1] == "same" && len(os.Args) == 4:
		same(c, page, snap, os.Args[3])
	default:
		fatal(usage)
	}
	if len(c.failures) > 0 {
		fatal(strings.Join(c.failures, "\n"))
	}
	fmt.Println("ok")
}

func finished(c *checker, page []byte, snap *export.Snapshot, allStepsPath string) {
	c.check(snap.Complete, "complete is false")

	nodes := nodesByName(snap)
	for _, name := range append(stagesBeforeSnapshot, "Snapshot", "After", "Post Actions") {
		c.check(nodes[name] != nil, "stage %q missing", name)
	}

	raw, err := os.ReadFile(allStepsPath)
	if err != nil {
		fatal(err.Error())
	}
	var pgv jenkins.PGVResponse
	if err := json.Unmarshal(raw, &pgv); err != nil {
		fatal("decoding " + allStepsPath + ": " + err.Error())
	}
	exported := map[string]bool{}
	snap.EachStep(func(_ *export.Node, s *export.Step) { exported[s.ID] = true })
	c.check(len(pgv.Data.Steps) > 0, "PGV lists no steps")
	for _, s := range pgv.Data.Steps {
		c.check(exported[s.ID], "step %s (%s) from allSteps missing", s.ID, s.Name)
	}

	checkLongLog(c, snap)

	failed := findStep(snap, "exit 3")
	c.check(failed != nil, "failing step missing")
	if failed != nil {
		c.check(failed.State == "FAILURE", "failing step state %q, want FAILURE", failed.State)
		c.check(strings.Contains(failed.Exception, "exit code 3"), "failing step exception %q lacks the exit code", failed.Exception)
	}

	console := decode(c, "console", snap.Console)
	c.check(strings.Contains(console, "ERROR: script returned exit code 3"), "console lacks the catchError ERROR: line")
	c.check(snap.ConsoleError == "", "console error: %s", snap.ConsoleError)

	for _, m := range remoteRef.FindAll(page, -1) {
		c.check(false, "remote resource reference %q", m)
	}
}

func live(c *checker, snap *export.Snapshot) {
	c.check(!snap.Complete, "complete is true for a mid-run export")

	self := findStep(snap, "jkit export html")
	c.check(self != nil, "exporting step missing")
	if self != nil {
		c.check(self.Running, "exporting step not running (state %q)", self.State)
	}

	nodes := nodesByName(snap)
	for _, name := range stagesBeforeSnapshot {
		n := nodes[name]
		c.check(n != nil, "stage %q missing", name)
		if n == nil {
			continue
		}
		c.check(!n.Running, "stage %q still running", name)
		c.check(len(n.Children) > 0 || len(n.Steps) > 0, "leaf stage %q has no steps", name)
		for i := range n.Steps {
			c.check(decode(c, n.Name+" step", n.Steps[i].Log) != "", "stage %q step %q has no log", name, n.Steps[i].Name)
		}
	}
	if after := nodes["After"]; after != nil {
		c.check(len(after.Steps) == 0, "stage After has steps before it started")
	}
	checkLongLog(c, snap)
}

// same names each step whose log, exception or error differs, then requires
// the whole pages to match but for the export time.
func same(c *checker, page []byte, snap *export.Snapshot, otherPath string) {
	other, err := os.ReadFile(otherPath)
	if err != nil {
		fatal(err.Error())
	}
	otherSnap, err := snapshotOf(other)
	if err != nil {
		fatal(err.Error())
	}

	type stepText struct{ log, exception, err string }
	texts := func(s *export.Snapshot) (ids []string, m map[string]stepText) {
		m = map[string]stepText{}
		s.EachStep(func(_ *export.Node, st *export.Step) {
			ids = append(ids, st.ID)
			m[st.ID] = stepText{decode(c, "step "+st.ID, st.Log), st.Exception, st.Error}
		})
		return ids, m
	}
	ids, a := texts(snap)
	otherIDs, b := texts(otherSnap)
	c.check(len(ids) > 0, "no steps")
	c.check(strings.Join(ids, ",") == strings.Join(otherIDs, ","), "step ids differ: %v vs %v", ids, otherIDs)
	for _, id := range ids {
		c.check(a[id] == b[id], "step %s differs:\n%q\nvs\n%q", id, a[id], b[id])
	}
	c.check(decode(c, "console", snap.Console) == decode(c, "console", otherSnap.Console), "consoles differ")
	c.check(snap.ConsoleError == "" && otherSnap.ConsoleError == "", "console errors: %q, %q", snap.ConsoleError, otherSnap.ConsoleError)

	mask := func(p []byte) []byte { return exportedAt.ReplaceAll(p, []byte(`"exportedAtMillis":0`)) }
	c.check(bytes.Equal(mask(page), mask(other)), "pages differ beyond exportedAtMillis")
}

func checkLongLog(c *checker, snap *export.Snapshot) {
	s := findStep(snap, "seq 1 20000")
	c.check(s != nil, "long-log step missing")
	if s == nil {
		return
	}
	text := decode(c, "long log", s.Log)
	n := 0
	for line := range strings.Lines(text) {
		if numbered.MatchString(strings.TrimSpace(line)) {
			n++
		}
	}
	c.check(n == longLines, "long log has %d numbered lines, want %d", n, longLines)
	c.check(!s.Log.Partial, "long log marked partial")
}

func snapshotOf(page []byte) (*export.Snapshot, error) {
	_, rest, ok := bytes.Cut(page, []byte(snapshotOpen))
	if !ok {
		return nil, fmt.Errorf("no snapshot script element")
	}
	data, _, ok := bytes.Cut(rest, []byte("</script>"))
	if !ok {
		return nil, fmt.Errorf("snapshot script element not closed")
	}
	var snap export.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decoding snapshot: %w", err)
	}
	return &snap, nil
}

func decode(c *checker, what string, l export.Log) string {
	if l.GzipBase64 == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(l.GzipBase64)
	if err != nil {
		c.check(false, "%s: base64: %v", what, err)
		return ""
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		c.check(false, "%s: gzip: %v", what, err)
		return ""
	}
	text, err := io.ReadAll(zr)
	if err != nil {
		c.check(false, "%s: gunzip: %v", what, err)
	}
	return string(text)
}

func nodesByName(snap *export.Snapshot) map[string]*export.Node {
	m := map[string]*export.Node{}
	var walk func([]export.Node)
	walk = func(nodes []export.Node) {
		for i := range nodes {
			m[nodes[i].Name] = &nodes[i]
			walk(nodes[i].Children)
		}
	}
	walk(snap.Stages)
	return m
}

// findStep returns the step whose name, the sh script, contains part.
func findStep(snap *export.Snapshot, part string) *export.Step {
	var found *export.Step
	snap.EachStep(func(_ *export.Node, s *export.Step) {
		if found == nil && strings.Contains(s.Name, part) {
			found = s
		}
	})
	return found
}

func fatal(msg string) {
	_, _ = fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
