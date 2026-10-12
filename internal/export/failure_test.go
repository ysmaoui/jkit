package export

import (
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
	id, msg := f.resolve(consolePipelineStages())
	assert.Equal(t, "20", id)
	assert.Equal(t, "script returned exit code 3", msg)
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
		id, msg := f.resolve(stages)
		assert.Equal(t, "71", id, "split at %d", i)
		assert.Equal(t, "Could not find credentials entry with ID 'SECRET'", msg, "split at %d", i)
	}
}

// An uncaught exception other than AbortException ends the build with a
// build-level stack trace and no "ERROR:" line. The catchError stage before
// it is then not the answer.
func TestFailureFinderUncaughtException(t *testing.T) {
	const trace = "[2026-10-12T11:13:21.000Z] groovy.lang.MissingPropertyException: No such property: foo for class: WorkflowScript\n" +
		"[2026-10-12T11:13:21.000Z] \tat WorkflowScript.run(WorkflowScript:12)\n"
	stages := append(consolePipelineStages(), Node{ID: "71", Name: "B", State: "FAILURE", StartTimeMillis: 5})
	tests := map[string]struct {
		build, msg string
	}{
		"exception":         {trace, "groovy.lang.MissingPropertyException: No such property: foo for class: WorkflowScript"},
		"Also first":        {"Also:   org.jenkinsci.plugins.workflow.actions.ErrorAction$ErrorId: 1f\n" + trace, "groovy.lang.MissingPropertyException: No such property: foo for class: WorkflowScript"},
		"Also only":         {"Also:   something\n", ""},
		"build ERROR line":  {"ERROR: script returned exit code 1\n", "script returned exit code 1"},
		"plain Error text":  {"Error: not a stack trace\nNo Exception here\n", "script returned exit code 3"},
		"unqualified class": {"MissingPropertyException: no package\n", "script returned exit code 3"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := findFailure(t, consolePipeline(t, "", tc.build))
			id, msg := f.resolve(stages)
			assert.Equal(t, tc.msg, msg)
			if tc.msg == "script returned exit code 3" {
				assert.Equal(t, "20", id, "no build-level failure")
				return
			}
			assert.Empty(t, id, "the build's own failure")
			s := &Snapshot{Result: "FAILURE", Stages: stages}
			s.markFailure(f)
			assert.Equal(t, "71", s.FailureNodeID, "falls back to the last failed leaf")
			assert.Equal(t, tc.msg, s.FailureMessage)
			wantSource := ""
			if tc.msg != "" {
				wantSource = "build"
			}
			assert.Equal(t, wantSource, s.FailureMessageSource)
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
	id, msg := f.resolve(stages)
	assert.Equal(t, "20", id)
	assert.Equal(t, "script returned exit code 3", msg)
}

func TestFailureFinderLines(t *testing.T) {
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
	assert.Equal(t, "node", snap.FailureMessageSource)

	snap, err = collect(t, client, run.job, Options{LogSource: LogSourceSteps})
	require.NoError(t, err)
	assert.Equal(t, "18", snap.FailureNodeID)
	assert.Empty(t, snap.FailureMessage)
	assert.Empty(t, snap.FailureMessageSource)
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

	tests := map[string]struct {
		result   string
		building bool
		find     *failureFinder
		id, msg  string
		src      string
	}{
		"attributed":                {"FAILURE", false, finder("12", "ERROR: in 9\n", "12", "9"), "9", "in 9", "node"},
		"stage not failed":          {"FAILURE", false, finder("12", "ERROR: in 10\n", "12", "10"), "8", "", ""},
		"build level falls back":    {"FAILURE", false, finder("", "ERROR: uncaught\n"), "8", "uncaught", "build"},
		"outside stages falls back": {"FAILURE", false, finder("99", "ERROR: outside\n"), "8", "", ""},
		"no error line":             {"FAILURE", false, newFailureFinder(), "8", "", ""},
		"steps path":                {"FAILURE", false, nil, "8", "", ""},
		"running":                   {"", true, nil, "8", "", ""},
		"success":                   {"SUCCESS", false, finder("12", "ERROR: in 9\n", "12", "9"), "", "", ""},
		"unstable":                  {"UNSTABLE", false, finder("12", "ERROR: in 9\n", "12", "9"), "", "", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := &Snapshot{Result: tc.result, Building: tc.building, Stages: stages()}
			s.markFailure(tc.find)
			assert.Equal(t, tc.id, s.FailureNodeID)
			assert.Equal(t, tc.msg, s.FailureMessage)
			assert.Equal(t, tc.src, s.FailureMessageSource)
		})
	}
	s := &Snapshot{Result: "FAILURE", Stages: []Node{{ID: "1", State: "SUCCESS"}}}
	s.markFailure(nil)
	assert.Empty(t, s.FailureNodeID, "no failed stage")
}
