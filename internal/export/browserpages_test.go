package export

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBrowserPages writes the pages tests/browser runs against into
// $JKIT_BROWSER_PAGES. Skipped without it; make browser-test sets it.
func TestBrowserPages(t *testing.T) {
	dir := os.Getenv("JKIT_BROWSER_PAGES")
	if dir == "" {
		t.Skip("JKIT_BROWSER_PAGES not set")
	}
	write := func(name string, s *Snapshot) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(renderString(t, s)), 0o644))
	}
	write("golden.html", renderFixture(t))
	// A build that threw before any stage, and a failure node the graph lacks:
	// both show only the build's message.
	for name, v := range map[string]struct{ node, msg string }{
		"golden-nonode.html":  {"", ""},
		"golden-missing.html": {"999", "script returned exit code 1"},
	} {
		s := renderFixture(t)
		s.FailureNodeID, s.FailureMessage = v.node, v.msg
		s.BuildErrorMessage = "hudson.AbortException: thrown before any stage"
		write(name, s)
	}
	// A running build whose console names no error: the page opens on the
	// failed leaf that started last but does not call it the build's failure.
	s := renderFixture(t)
	s.FailureNodeID, s.FailureMessage, s.BuildErrorMessage = "", "", ""
	s.markFailure(newFailureFinder())
	require.Equal(t, "14", s.FailureNodeID)
	write("golden-running.html", s)
	finished := func(s *Snapshot) { s.Building, s.Complete, s.Result = false, true, "FAILURE" }
	// unit (14) failed under catchError, then Setup (20), which started after
	// it, threw: the page opens on Setup with no stage message.
	s = renderFixture(t)
	finished(s)
	s.Stages[1].Children[1].Children[0].State = "FAILURE"
	// Setup has no failed step, so the page offers a console search for it.
	s.Console = mustLog(t, testSpool(t), 0, "Started by user admin\n[Pipeline] { (Setup)\nERROR: script returned exit code 2\nFinished: FAILURE\n")
	f := newFailureFinder()
	f.newNode("90", "", "14")
	f.text("90", []byte("ERROR: script returned exit code 1\n"))
	f.text("", []byte("ERROR: script returned exit code 2\n"))
	s.markFailure(f)
	require.Equal(t, "20", s.FailureNodeID)
	require.Empty(t, s.FailureMessage)
	require.Equal(t, "script returned exit code 2", s.BuildErrorMessage)
	write("golden-later.html", s)
	// Setup (20) failed under catchError, then the unit branch (14), which
	// started before it, threw: the branch's closing node names it. The
	// failure reached the build, so Test, which encloses unit, failed too.
	s = renderFixture(t)
	finished(s)
	s.Stages[1].Children[1].Children[0].State = "FAILURE"
	s.Stages[1].State, s.Stages[1].Running = "FAILURE", false
	s.Console = mustLog(t, testSpool(t), 0, "Started by user admin\n[Pipeline] { (Setup)\nERROR: caught\nFailed in branch unit\nERROR: script returned exit code 1\nFinished: FAILURE\n")
	f = newFailureFinder()
	f.newNode("90", "", "20")
	f.newNode("91", "14", "")
	f.text("90", []byte("ERROR: caught\n"))
	f.text("91", []byte("Failed in branch unit\n"))
	f.text("", []byte("ERROR: script returned exit code 1\n"))
	s.markFailure(f)
	require.Equal(t, "14", s.FailureNodeID)
	require.Empty(t, s.FailureMessage)
	require.Equal(t, "script returned exit code 1", s.BuildErrorMessage)
	write("golden-branch.html", s)
	write("wide-failed.html", wideFixture(t, true))
	write("wide-passed.html", wideFixture(t, false))
}

// failingStage fails in every branch of the failed wide fixture.
const failingStage = "Validate outputs"

// wideFixture is a run too wide for one screen, shaped like large real
// builds: dozens of top-level stages, parallel blocks of up to eleven
// branches with nested stages, so the page auto-collapses branches. With
// failed, one stage per branch fails plus two top-level ones, the last of
// which has no failed step and is named by a build-level message.
func wideFixture(t *testing.T, failed bool) *Snapshot {
	t.Helper()
	const t0 = int64(1791791176000)
	sp := testSpool(t)
	id, clock := 100, t0
	next := func() string { id += 7; return strconv.Itoa(id) }
	// failLast marks the last step failed; without it a FAILURE stage has no failed step.
	steps := func(n int, failLast bool) []Step {
		out := make([]Step, n)
		for i := range out {
			clock += 900
			out[i] = Step{ID: next(), Name: fmt.Sprintf("sh ./ci/step-%d.sh --verbose", i+1), State: "SUCCESS", StartTimeMillis: clock, DurationMillis: 800,
				Log: mustLog(t, sp, 0, strings.Repeat(fmt.Sprintf("step %d output line\n", i+1), 20))}
			if failLast && i == n-1 {
				out[i].State, out[i].Exception = "FAILURE", "script returned exit code 2"
			}
		}
		return out
	}
	stage := func(name, state string, failLast bool) Node {
		n := Node{ID: next(), Name: name, Type: "STAGE", State: state, StartTimeMillis: clock, DurationMillis: 4000, Agent: "linux-build-07", Children: []Node{}, Steps: []Step{}}
		if state == "NOT_BUILT" {
			n.StartTimeMillis, n.DurationMillis = 0, 0
		} else {
			n.Steps = steps(3, failLast)
		}
		return n
	}
	ok := func(name string) Node { return stage(name, "SUCCESS", false) }
	// skipped names nested stages that did not run in this branch.
	branch := func(name string, kids []string, skipped ...string) Node {
		b := Node{ID: next(), Name: name, Type: "PARALLEL", State: "SUCCESS", StartTimeMillis: clock, DurationMillis: 60_000, Children: []Node{}}
		b.Steps = steps(1, false)
		for _, k := range kids {
			switch {
			case failed && k == failingStage:
				b.Children = append(b.Children, stage(k, "FAILURE", true))
				b.State = "FAILURE"
			case slices.Contains(skipped, k):
				b.Children = append(b.Children, stage(k, "NOT_BUILT", false))
			default:
				b.Children = append(b.Children, ok(k))
			}
		}
		return b
	}
	block := func(branches ...Node) Node {
		n := Node{ID: next(), Name: "Parallel", Type: "PARALLEL_BLOCK", State: "SUCCESS", StartTimeMillis: clock, DurationMillis: 90_000, Steps: []Step{}, Children: branches}
		for _, b := range branches {
			if b.State == "FAILURE" {
				n.State = "FAILURE"
			}
		}
		return n
	}

	s := &Snapshot{
		Job:              "platform/release/main",
		Number:           1187,
		StartTimeMillis:  t0,
		URL:              "https://jenkins.example.com/job/platform/job/release/job/main/1187/",
		Complete:         true,
		ExportedAtMillis: t0 + 3_600_000,
		DurationMillis:   3_500_000,
	}
	var console strings.Builder
	console.WriteString("Started by timer\n[Pipeline] Start of Pipeline\n")
	if failed {
		s.Result = "FAILURE"
		kids := []string{"Resolve source revision", "Evaluate parameters", "Merge job parameters", "Check required parameters",
			"Build", "Collect results", failingStage, "Check configuration", "Send status notice", "Upload artifacts", "Publish summary report"}
		nmod := 0
		mod := func(m string) Node {
			k := append([]string(nil), kids...)
			k[4] = "Build - service-mod-" + m + "-src"
			nmod++
			if nmod%2 == 0 {
				return branch("service-mod-"+m+"-src", k)
			}
			return branch("service-mod-"+m+"-src", k, "Send status notice", "Publish summary report")
		}
		s.Stages = []Node{ok("Configure source control"), ok("Prepare configuration"), ok("Start cache"), ok("Create temporary work branches"), ok("Prepare build data"),
			stage(failingStage, "FAILURE", true), ok("Check configuration"), ok("Upload metadata"),
			block(mod("alpha"), mod("bravo"), mod("charlie")),
			block(mod("delta")),
			block(mod("echo"), mod("foxtrot")),
			block(mod("golf"), mod("hotel"), mod("india")),
			block(mod("juliett")),
			block(mod("kilo")),
		}
		up := ok("Upload artifacts")
		up.Children = []Node{ok("Upload artifacts")}
		up.Steps = []Step{}
		s.Stages = append(s.Stages, up, ok("Generate release notes"), ok("Publish documentation to the internal portal"), ok("Notify subscribers"),
			ok("Archive build metadata"), ok("Trigger downstream integration jobs"), ok("Record dependency graph"), ok("Update dashboards"),
			ok("Tag release candidate"), ok("Promote artifacts"), ok("Sync mirrors"), ok("Verify signatures"))
		quiet := stage("Publish run statistics", "FAILURE", false)
		s.Stages = append(s.Stages, quiet, ok("Clean up"))
		s.FailureNodeID = quiet.ID
		s.BuildErrorMessage = "org.jenkinsci.plugins.scriptsecurity.sandbox.RejectedAccessException: Scripts not permitted to use staticMethod java.lang.System getenv"
		console.WriteString("[Pipeline] { (Publish run statistics)\nPublish run statistics: posting 42 records\n" + s.BuildErrorMessage + "\nFinished: FAILURE\n")
	} else {
		s.Result = "SUCCESS"
		kinds := []string{"BuildPackages", "BuildPackagesDebug", "BuildPackages", "UnitTests", "StaticChecks", "FormatCheck", "Docs"}
		var bs []Node
		for i := range 11 {
			bs = append(bs, branch(fmt.Sprintf("target-platform-variant-%02d::%s", i+1, kinds[i%len(kinds)]), []string{"[Prepare node]", "[Init module]",
				"[Set up packages]", "[Set up tooling]", "Conformity checks", "Build packages", "[Quality gate]", "[Parse logs]", "[Export artifacts]", "[Finish module]"}))
		}
		s.Stages = []Node{ok("Init pipeline"), ok("Read configuration"), ok("Init reporting"), ok("Checkout"), ok("Setup"), ok("Load dependencies"),
			ok("Determine variants"), ok("Determine modules"), ok("Create workspace snapshot"), block(bs...), ok("Merge results"), ok("Evaluate quality gates"),
			ok("Upload packages to the artifact store"), ok("Write release notes"), ok("Finish")}
		console.WriteString("Finished: SUCCESS\n")
	}
	s.Console = mustLog(t, sp, 0, console.String())
	return s
}
