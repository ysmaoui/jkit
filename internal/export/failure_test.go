package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFailure runs html through a consoleSplitter feeding a failureFinder,
// in the given writes.
func findFailure(t *testing.T, writes ...string) *failureFinder {
	t.Helper()
	f := newFailureFinder()
	s := newConsoleSplitter(func(node string, text []byte) error {
		f.text(node, text)
		return nil
	})
	s.newNode = f.newNode
	for _, w := range writes {
		_, err := s.Write([]byte(w))
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	return f
}

// lastError is the node and text of the latest error line f saw, node ""
// for the build's own.
func lastError(f *failureFinder) (node, msg string) {
	var last errorLine
	for n, e := range f.byNode {
		if e.seq > last.seq {
			last, node = e, n
		}
	}
	if l := len(f.build); l > 0 && f.build[l-1].seq > last.seq {
		last, node = f.build[l-1], ""
	}
	return node, last.msg
}

// consolePipelineStages is the stage tree of the console-pipeline fixture:
// PGV names a stage by the node of its labelled body, "{ (Text)".
func consolePipelineStages() []Node {
	return []Node{
		{ID: "8", Name: "Text", State: "SUCCESS"},
		{ID: "20", Name: "Flaky", State: "FAILURE", StartTimeMillis: 2},
		{ID: "29", Name: "Fan Out", State: "SUCCESS", Children: []Node{
			{ID: "32", Name: "Branch A", Type: "PARALLEL", Children: []Node{{ID: "35", Name: "Branch A"}}},
			{ID: "33", Name: "Branch B", Type: "PARALLEL", Children: []Node{
				{ID: "37", Name: "Branch B", Children: []Node{{ID: "39", Name: "B1"}, {ID: "49", Name: "B2"}}},
			}},
		}},
		{ID: "60", Name: "Declarative: Post Actions", Synthetic: true},
	}
}

// consolePipeline is the console-pipeline fixture with stage inserted after
// its last stage, build appended to the build's own text, and a failed
// result.
func consolePipeline(t *testing.T, stage, build string) string {
	t.Helper()
	html, _ := readConsoleFixture(t, "console-pipeline")
	const at = `<span class="pipeline-new-node" nodeId="64" startId="6">`
	const end = "Finished: SUCCESS"
	require.Contains(t, string(html), at)
	require.Contains(t, string(html), end)
	s := strings.Replace(string(html), at, stage+at, 1)
	return strings.Replace(s, end, build+"Finished: FAILURE", 1)
}

// The catchError body's closing "}" writes the error, a node no step listing
// holds; it traces back through catchError to the stage.
func TestFailureFinderBlockEndNode(t *testing.T) {
	html, _ := readConsoleFixture(t, "console-pipeline")
	f := findFailure(t, string(html))
	node, msg := lastError(f)
	assert.Equal(t, "24", node)
	assert.Equal(t, "script returned exit code 3", msg)
	id, msg, build := f.resolve(consolePipelineStages())
	assert.Equal(t, "20", id)
	assert.Equal(t, "script returned exit code 3", msg)
	assert.Empty(t, build)
}

// lateFailure is a stage after the fixture's catchError stage whose error
// only its catchError body's closing node writes, as when withCredentials
// names a missing credential.
const lateFailure = `<span class="pipeline-new-node" nodeId="70" startId="70" enclosingId="6">[Pipeline] stage
</span><span class="pipeline-new-node" nodeId="71" startId="71" enclosingId="70" label="Upload">[Pipeline] { (Upload)
</span><span class="pipeline-new-node" nodeId="72" startId="72" enclosingId="71">[Pipeline] catchError
</span><span class="pipeline-new-node" nodeId="73" startId="73" enclosingId="72">[Pipeline] {
</span><span class="pipeline-new-node" nodeId="74" startId="74" enclosingId="73">[Pipeline] withCredentials
</span><span class="pipeline-new-node" nodeId="75" startId="74">[Pipeline] // withCredentials
</span><span class="pipeline-new-node" nodeId="76" startId="73">[Pipeline] }
</span><span class="pipeline-node-76"><span class="timestamp"><b>11:13:21</b> </span><span style="display: none">[2026-10-12T11:13:21.000Z]</span> ERROR: [ DEBUG ] stage 'Upload' failed
<span class="timestamp"><b>11:13:21</b> </span><span style="display: none">[2026-10-12T11:13:21.000Z]</span> ERROR: Could not find credentials entry with ID &#039;SECRET&#039;
</span><span class="pipeline-new-node" nodeId="77" startId="72">[Pipeline] // catchError
</span><span class="pipeline-new-node" nodeId="78" startId="71">[Pipeline] }
</span><span class="pipeline-new-node" nodeId="79" startId="70">[Pipeline] // stage
</span>`

func TestFailureFinderCatchErrorThenRealFailure(t *testing.T) {
	html := consolePipeline(t, lateFailure, "")
	stages := append(consolePipelineStages(), Node{ID: "71", Name: "Upload", State: "FAILURE", StartTimeMillis: 1})
	// Every split point gives the same answer: lines cross writes.
	for i := 0; i <= len(html); i += 97 {
		f := findFailure(t, html[:i], html[i:])
		id, msg, build := f.resolve(stages)
		assert.Equal(t, "71", id, "split at %d", i)
		assert.Equal(t, "Could not find credentials entry with ID 'SECRET'", msg, "split at %d", i)
		assert.Empty(t, build, "split at %d", i)
	}
}

const uncaughtTrace = "[2026-10-12T11:13:21.000Z] groovy.lang.MissingPropertyException: No such property: foo for class: WorkflowScript\n" +
	"[2026-10-12T11:13:21.000Z] \tat WorkflowScript.run(WorkflowScript:12)\n"

const uncaughtHeader = "groovy.lang.MissingPropertyException: No such property: foo for class: WorkflowScript"

// An uncaught exception other than AbortException ends the build with a
// build-level stack trace and no "ERROR:" line; an uncaught sh or error step
// writes a build-level "ERROR:" line. Either is kept apart from the
// catchError stage's error before it. That stage keeps the failure unless a
// failed stage started after it, which is then what the build threw from.
func TestFailureFinderUncaughtException(t *testing.T) {
	tests := map[string]struct {
		build, want string
	}{
		"exception":          {uncaughtTrace, uncaughtHeader},
		"Also first":         {"Also:   org.jenkinsci.plugins.workflow.actions.ErrorAction$ErrorId: 1f\n" + uncaughtTrace, uncaughtHeader},
		"Also ignored":       {"Also:   something\n", ""},
		"uncaught sh":        {"ERROR: script returned exit code 2\n", "script returned exit code 2"},
		"uncaught error":     {"ERROR: later stage fail\n", "later stage fail"},
		"repeats stage line": {"ERROR: script returned exit code 3\n", "script returned exit code 3"},
		"plain Error text":   {"Error: not a stack trace\nNo Exception here\n", ""},
		"unqualified class":  {"MissingPropertyException: no package\n", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := findFailure(t, consolePipeline(t, "", tc.build))

			// Flaky (20) is the only failed stage: it keeps the failure, as
			// when post-pipeline code throws.
			id, msg, build := f.resolve(consolePipelineStages())
			assert.Equal(t, "20", id, "the stage error keeps its stage")
			assert.Equal(t, "script returned exit code 3", msg)
			if tc.want == msg {
				assert.Empty(t, build, "a build line repeating the stage's is dropped")
			} else {
				assert.Equal(t, tc.want, build)
			}

			// B failed after Flaky's caught error: the build threw from B.
			stages := append(consolePipelineStages(), Node{ID: "71", Name: "B", State: "FAILURE", StartTimeMillis: 5})
			s := &Snapshot{Result: "FAILURE", Stages: stages}
			s.markFailure(f)
			if tc.want == "" {
				assert.Equal(t, "20", s.FailureNodeID, "no build-level failure")
				assert.Equal(t, "script returned exit code 3", s.FailureMessage)
			} else {
				assert.Equal(t, "71", s.FailureNodeID, "the later failed stage threw")
				assert.Empty(t, s.FailureMessage, "the uncaught error is the build's")
			}
			assert.Equal(t, tc.want, s.BuildErrorMessage)

			// Flaky passed, so only the build's own error is left.
			stages[1].State = "SUCCESS"
			s = &Snapshot{Result: "FAILURE", Stages: stages}
			s.markFailure(f)
			assert.Equal(t, "71", s.FailureNodeID, "falls back to the last failed leaf")
			assert.Empty(t, s.FailureMessage, "the fallback leaf wrote no error")
			assert.Equal(t, tc.want, s.BuildErrorMessage)
		})
	}
}

// Post-pipeline code throwing after a catchError stage failed on a missing
// credential: the page needs both, the stage error on its stage and the
// exception for the build.
func TestFailureFinderStageErrorThenException(t *testing.T) {
	f := findFailure(t, consolePipeline(t, lateFailure, uncaughtTrace))
	s := &Snapshot{Result: "FAILURE", Stages: append(consolePipelineStages(), Node{ID: "71", Name: "Upload", State: "FAILURE", StartTimeMillis: 5})}
	s.markFailure(f)
	assert.Equal(t, "71", s.FailureNodeID)
	assert.Equal(t, "Could not find credentials entry with ID 'SECRET'", s.FailureMessage)
	assert.Equal(t, uncaughtHeader, s.BuildErrorMessage)
}

// testdata/failure holds progressiveHtml captured from the integration
// Jenkins running the .groovy beside each page. Branch X catches an error
// while branch Y throws and fails the build; start times do not tell which
// threw.
func TestFailureFinderParallelLaterBranch(t *testing.T) {
	const t0 = int64(1791867641000)
	tests := map[string]struct {
		page   string
		stages []Node
		want   string
	}{
		"declarative": {"parallel-later", []Node{{ID: "6", Name: "P", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
			{ID: "9", Name: "X", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0 + 86, Children: []Node{
				{ID: "16", Name: "X1", State: "SUCCESS", StartTimeMillis: t0 + 221},
				{ID: "24", Name: "X2", State: "FAILURE", StartTimeMillis: t0 + 1320},
			}},
			{ID: "10", Name: "Y", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0 + 94, Children: []Node{
				{ID: "18", Name: "Y1", State: "FAILURE", StartTimeMillis: t0 + 238},
			}},
		}}}, "18"},
		// Siblings often start in the same millisecond.
		"scripted": {"scripted-parallel-later", []Node{{ID: "5", Name: "Parallel", Type: "PARALLEL_BLOCK", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
			{ID: "7", Name: "X", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "10", Name: "X1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
			{ID: "8", Name: "Y", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "12", Name: "Y1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
		}}}, "12"},
		// Y throws before X catches its error, so Y's branch line comes
		// first; Y is X's sibling, so it still failed the parallel.
		"sibling line first": {"sibling-before-caught", []Node{{ID: "5", Name: "Parallel", Type: "PARALLEL_BLOCK", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
			{ID: "7", Name: "Y", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "10", Name: "Y1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
			{ID: "8", Name: "X", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "12", Name: "X1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
		}}}, "10"},
		"sibling line first, X declared first": {"sibling-before-caught-swapped", []Node{{ID: "5", Name: "Parallel", Type: "PARALLEL_BLOCK", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
			{ID: "7", Name: "X", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "10", Name: "X1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
			{ID: "8", Name: "Y", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0, Children: []Node{
				{ID: "12", Name: "Y1", State: "FAILURE", StartTimeMillis: t0 + 30},
			}},
		}}}, "12"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			html, err := os.ReadFile(filepath.Join("testdata", "failure", tc.page+".html"))
			require.NoError(t, err)
			s := &Snapshot{Result: "FAILURE", Stages: tc.stages}
			s.markFailure(findFailure(t, string(html)))
			assert.Equal(t, tc.want, s.FailureNodeID, "Y1 threw")
			assert.Empty(t, s.FailureMessage, "the caught error is X's")
			assert.Equal(t, "real", s.BuildErrorMessage)
		})
	}
}

// These fixtures carry the stage tree the export built for the run, with
// its real start times.
func TestFailureFinderCapturedRuns(t *testing.T) {
	tests := map[string]struct{ id, name string }{
		// The caught stage and the one that threw share branch O1.
		"later-stage-in-branch": {"25", "Z"},
		// X1 sits three parallels deep; E threw first.
		"deep-nested-branch": {"14", "E1"},
		// Y's branch line follows the caught error, but a try/catch swallowed
		// that parallel and B threw.
		"swallowed-parallel": {"37", "B"},
	}
	for page, tc := range tests {
		t.Run(page, func(t *testing.T) {
			html, err := os.ReadFile(filepath.Join("testdata", "failure", page+".html"))
			require.NoError(t, err)
			b, err := os.ReadFile(filepath.Join("testdata", "failure", page+".stages.json"))
			require.NoError(t, err)
			s := &Snapshot{Result: "FAILURE"}
			require.NoError(t, json.Unmarshal(b, &s.Stages))
			s.markFailure(findFailure(t, string(html)))
			assert.Equal(t, tc.id, s.FailureNodeID, tc.name)
			assert.Empty(t, s.FailureMessage)
			assert.Equal(t, "real", s.BuildErrorMessage)
		})
	}
}

func TestFailureFinderBranchLines(t *testing.T) {
	// R's branch Q failed inside a try/catch before P ran; in P, X1 caught
	// an error, Y threw and failFast aborted Z.
	stages := func() []Node {
		return []Node{
			{ID: "1", Name: "R", Type: "PARALLEL_BLOCK", State: "SUCCESS", StartTimeMillis: 10, Children: []Node{
				{ID: "2", Name: "Q", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: 10, Children: []Node{{ID: "3", Name: "Q1", State: "FAILURE", StartTimeMillis: 10}}},
			}},
			{ID: "4", Name: "P", Type: "PARALLEL_BLOCK", State: "FAILURE", StartTimeMillis: 20, Children: []Node{
				{ID: "5", Name: "X", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: 20, Children: []Node{{ID: "6", Name: "X1", State: "FAILURE", StartTimeMillis: 20}}},
				{ID: "7", Name: "Y", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: 20, Children: []Node{{ID: "8", Name: "Y1", State: "FAILURE", StartTimeMillis: 20}}},
				{ID: "9", Name: "Z", Type: "PARALLEL", State: "ABORTED", StartTimeMillis: 20, Children: []Node{{ID: "10", Name: "Z1", State: "ABORTED", StartTimeMillis: 20}}},
			}},
		}
	}
	type write struct{ node, text string }
	tests := map[string]struct {
		writes []write
		id     string
	}{
		"other parallel's earlier line ignored": {[]write{{"92", "Failed in branch Q\n"}, {"90", "ERROR: caught\n"}, {"", "ERROR: real\n"}}, "6"},
		"aborted branch skipped":                {[]write{{"90", "ERROR: caught\n"}, {"94", "Failed in branch Z\n"}, {"93", "Failed in branch Y\n"}, {"", "ERROR: real\n"}}, "8"},
		"sibling's earlier line counts":         {[]write{{"93", "Failed in branch Y\n"}, {"90", "ERROR: caught\n"}, {"", "ERROR: real\n"}}, "8"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFailureFinder()
			f.newNode("90", "", "6")
			f.newNode("92", "2", "")
			f.newNode("93", "7", "")
			f.newNode("94", "9", "")
			for _, w := range tc.writes {
				f.text(w.node, []byte(w.text))
			}
			s := &Snapshot{Result: "FAILURE", Stages: stages()}
			s.markFailure(f)
			assert.Equal(t, tc.id, s.FailureNodeID)
			assert.Equal(t, "real", s.BuildErrorMessage)
		})
	}
}

// A green step that echoes an "ERROR:" line after the real failure does not
// take it over.
func TestFailureFinderGreenStepEcho(t *testing.T) {
	const echo = `<span class="pipeline-new-node" nodeId="80" startId="80" enclosingId="6">[Pipeline] stage
</span><span class="pipeline-new-node" nodeId="81" startId="81" enclosingId="80" label="Report">[Pipeline] { (Report)
</span><span class="pipeline-new-node" nodeId="82" enclosingId="81">[Pipeline] echo
</span><span class="pipeline-node-82"><span class="timestamp"><b>11:13:22</b> </span><span style="display: none">[2026-10-12T11:13:22.000Z]</span> ERROR: lines in the report are fine
</span><span class="pipeline-new-node" nodeId="83" startId="81">[Pipeline] }
</span><span class="pipeline-new-node" nodeId="84" startId="80">[Pipeline] // stage
</span>`
	f := findFailure(t, consolePipeline(t, echo, ""))
	node, _ := lastError(f)
	require.Equal(t, "82", node)
	stages := append(consolePipelineStages(), Node{ID: "81", Name: "Report", State: "SUCCESS", StartTimeMillis: 9})
	id, msg, build := f.resolve(stages)
	assert.Equal(t, "20", id)
	assert.Equal(t, "script returned exit code 3", msg)
	assert.Empty(t, build)
}

func TestFailureFinderLines(t *testing.T) {
	// The splitter needs one node span to accept the page.
	const span = `<span class="pipeline-node-3">ok` + "\n</span>"
	tests := map[string]struct {
		in, node, msg string
	}{
		"unended line":       {`<span class="pipeline-node-3">ERROR: x</span>` + "\n", "", ""},
		"line ends in span":  {`<span class="pipeline-node-3">ERROR: x` + "\n</span>", "3", "x"},
		"timestamped":        {`<span class="pipeline-node-3">[2026-10-12T11:13:16.556Z] ERROR: x` + "\n</span>", "3", "x"},
		"other bracket":      {`<span class="pipeline-node-3">[main] ERROR: x` + "\n</span>", "", ""},
		"build text":         {`<span class="pipeline-node-3">ERROR: x` + "\n</span>ERROR: build failed\nFinished: FAILURE\n", "", "build failed"},
		"not at line start":  {`<span class="pipeline-node-3">+ echo ERROR: x` + "\n</span>", "", ""},
		"bracketed level":    {`<span class="pipeline-node-3">[app] [ERROR] x` + "\n</span>", "", ""},
		"ansi dropped":       {`<span class="pipeline-node-3">ERROR: <!--` + "\x1b[31m" + `-->red` + "\x1b[0m\n</span>", "3", "red"},
		"interleaved halves": {`<span class="pipeline-node-3">ERR</span><span class="pipeline-node-4">ok` + "\n</span>" + `<span class="pipeline-node-3">OR: y` + "\n</span>", "3", "y"},
		"step exception":     {`<span class="pipeline-node-3">java.io.IOException: x` + "\n</span>", "", ""},
		"header detail":      {span + "a.BException: startup failed:\n\n  Script: 6: bad\nmore\n", "", "a.BException: startup failed: Script: 6: bad"},
		"header then frame":  {span + "a.BException: failed:\n\tat a.B.c(B.java:1)\nmore\n", "", "a.BException: failed:"},
		"header then error":  {span + "a.BException: failed:\nERROR: x\n", "", "x"},
		"header with text":   {span + "a.BException: boom\nmore\n", "", "a.BException: boom"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			node, msg := lastError(findFailure(t, tc.in))
			assert.Equal(t, tc.node, node)
			assert.Equal(t, tc.msg, msg)
		})
	}
}

func TestFailureFinderLongLineCapped(t *testing.T) {
	long := strings.Repeat("x", 3*maxFailureLineBytes)
	f := findFailure(t, `<span class="pipeline-node-3">ERROR: `+long[:100], long[100:]+"\n</span>")
	node, msg := lastError(f)
	assert.Equal(t, "3", node)
	assert.Less(t, len(msg), maxFailureLineBytes)
}

// The scripted run's failed stage is Fail (18); an error written later
// inside it by a block's end node is what the console names, and Build (7),
// which did not fail, cannot take it.
func TestCollectFailureNode(t *testing.T) {
	run := consoleRun(t)
	run.consoleHTML = strings.Replace(run.consoleHTML, "[Pipeline] End of Pipeline",
		`<span class="pipeline-new-node" nodeId="99" startId="18">[Pipeline] }
</span><span class="pipeline-node-99">ERROR: late
</span><span class="pipeline-new-node" nodeId="100" startId="7">[Pipeline] }
</span><span class="pipeline-node-100">ERROR: green
</span>[Pipeline] End of Pipeline`, 1)
	client := run.serve(t)

	snap, err := collect(t, client, run.job, Options{LogSource: LogSourceConsole})
	require.NoError(t, err)
	assert.Equal(t, "18", snap.FailureNodeID)
	assert.Equal(t, "late", snap.FailureMessage)
	assert.Empty(t, snap.BuildErrorMessage)

	snap, err = collect(t, client, run.job, Options{LogSource: LogSourceSteps})
	require.NoError(t, err)
	assert.Equal(t, "18", snap.FailureNodeID)
	assert.Empty(t, snap.FailureMessage)
	assert.Empty(t, snap.BuildErrorMessage)
}

func TestMarkFailure(t *testing.T) {
	stages := func() []Node {
		return []Node{
			{ID: "7", State: "FAILURE", StartTimeMillis: 10, Children: []Node{
				{ID: "8", State: "FAILURE", StartTimeMillis: 30},
			}},
			{ID: "9", State: "FAILURE", StartTimeMillis: 20},
			{ID: "10", State: "SUCCESS", StartTimeMillis: 40},
		}
	}
	finder := func(node, text string, parents ...string) *failureFinder {
		f := newFailureFinder()
		for i := 0; i+1 < len(parents); i += 2 {
			f.newNode(parents[i], "", parents[i+1])
		}
		f.text(node, []byte(text))
		return f
	}

	// then writes text as node after what f already holds.
	then := func(f *failureFinder, node, text string) *failureFinder {
		f.text(node, []byte(text))
		return f
	}

	tests := map[string]struct {
		result       string
		building     bool
		find         *failureFinder
		id, msg, bld string
	}{
		"attributed":                {"FAILURE", false, finder("12", "ERROR: in 9\n", "12", "9"), "9", "in 9", ""},
		"stage and build":           {"FAILURE", false, then(finder("12", "ERROR: in 8\n", "12", "8"), "", "java.lang.IllegalStateException: boom\n"), "8", "in 8", "java.lang.IllegalStateException: boom"},
		"later stage threw":         {"FAILURE", false, then(finder("12", "ERROR: in 9\n", "12", "9"), "", "ERROR: script returned exit code 2\n"), "8", "", "script returned exit code 2"},
		"leaf inside stage threw":   {"FAILURE", false, then(finder("12", "ERROR: in 7\n", "12", "7"), "", "ERROR: in 8\n"), "7", "in 7", "in 8"},
		"later branch threw":        {"FAILURE", false, then(then(finder("12", "ERROR: in 8\n", "12", "8", "13", "9"), "13", "Failed in branch 9\n"), "", "ERROR: real\n"), "9", "", "real"},
		"own branch threw":          {"FAILURE", false, then(then(finder("12", "ERROR: in 9\n", "12", "9", "13", "9"), "13", "Failed in branch 9\n"), "", "ERROR: real\n"), "9", "in 9", "real"},
		"inner branch line first":   {"FAILURE", false, then(then(then(finder("12", "ERROR: in 8\n", "12", "8", "13", "9", "14", "7"), "13", "Failed in branch 9\n"), "14", "Failed in branch 7\n"), "", "ERROR: real\n"), "9", "", "real"},
		"branch line before error":  {"FAILURE", false, then(then(finder("13", "Failed in branch 9\n", "12", "9", "13", "9"), "12", "ERROR: in 9\n"), "", "ERROR: real\n"), "8", "", "real"},
		"build repeats stage":       {"FAILURE", false, then(finder("12", "ERROR: in 8\n", "12", "8"), "", "ERROR: in 8\n"), "8", "in 8", ""},
		"build before stage":        {"FAILURE", false, then(finder("", "ERROR: early\n", "12", "9"), "12", "ERROR: in 9\n"), "9", "in 9", ""},
		"stage not failed":          {"FAILURE", false, finder("12", "ERROR: in 10\n", "12", "10"), "8", "", ""},
		"build only falls back":     {"FAILURE", false, finder("", "ERROR: uncaught\n"), "8", "", "uncaught"},
		"outside stages falls back": {"FAILURE", false, finder("99", "ERROR: outside\n"), "8", "", ""},
		"no error line":             {"FAILURE", false, newFailureFinder(), "8", "", ""},
		"steps path":                {"FAILURE", false, nil, "8", "", ""},
		"running":                   {"", true, nil, "8", "", ""},
		"running caught error":      {"", true, finder("12", "ERROR: in 9\n", "12", "9"), "9", "in 9", ""},
		"success":                   {"SUCCESS", false, finder("12", "ERROR: in 9\n", "12", "9"), "", "", ""},
		"unstable":                  {"UNSTABLE", false, finder("12", "ERROR: in 9\n", "12", "9"), "", "", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := &Snapshot{Result: tc.result, Building: tc.building, Stages: stages()}
			s.markFailure(tc.find)
			assert.Equal(t, tc.id, s.FailureNodeID)
			assert.Equal(t, tc.msg, s.FailureMessage)
			assert.Equal(t, tc.bld, s.BuildErrorMessage)
		})
	}
	s := &Snapshot{Result: "FAILURE", Stages: []Node{{ID: "1", State: "SUCCESS"}}}
	s.markFailure(nil)
	assert.Empty(t, s.FailureNodeID, "no failed stage")
}

// A Jenkinsfile that fails to compile ends the build before any flow node
// exists: PGV answers an empty tree it never reports complete, and the
// compile error is build-level console text.
func TestCollectCompileFailure(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "failure", name))
		require.NoError(t, err)
		return string(b)
	}
	run := loadRun(t, "done", "compile")
	run.consoleHTML = read("compile-error.html")
	run.console = read("compile-error.txt")
	client := run.serve(t)

	var notices []string
	snap, err := collect(t, client, run.job, Options{Notice: func(msg string) { notices = append(notices, msg) }})
	require.NoError(t, err)
	assert.True(t, snap.Complete)
	assert.Empty(t, snap.Stages)
	assert.Empty(t, snap.FailureNodeID)
	assert.Equal(t, "org.codehaus.groovy.control.MultipleCompilationErrorsException: startup failed: WorkflowScript: 6: expecting '}', found '' @ line 6, column 2.", snap.BuildErrorMessage)
	assert.Equal(t, []string{"reading step logs from the annotated console"}, notices)
	assert.Contains(t, decode(t, snap.Console), "expecting '}'")
}

// A failed run whose stage tree and step listing PGV has not caught up on is
// still a snapshot, whatever its result.
func TestCollectFailedButIncomplete(t *testing.T) {
	run := consoleRun(t)
	run.tree = strings.Replace(run.tree, `"complete":true`, `"complete":false`, 1)
	run.allSteps = strings.Replace(run.allSteps, `"runIsComplete":true`, `"runIsComplete":false`, 1)
	require.Contains(t, run.tree, `"complete":false`)
	require.Contains(t, run.allSteps, `"runIsComplete":false`)
	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	assert.Equal(t, "FAILURE", snap.Result)
	assert.NotEmpty(t, snap.Stages)
	assert.False(t, snap.Complete)
	assert.Positive(t, run.stepLogHits.Load(), "steps the console does not prove whole are read on their own")
}
