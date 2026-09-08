package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scanJobClass = `{"_class":"org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject","jobs":[]}`

// scanLogFixture is a live-shaped GitHub branch source log with the scan time
// substituted, so a test can make the same run look fresh or weeks old.
func scanLogFixture(started time.Time) string {
	return "Started by timer\n[" + started.Format("Mon Jan 02 15:04:05 MST 2006") + "] Starting branch indexing...\n" +
		"Connecting to https://github.example.com/api/v3 using app credentials\n" +
		"Examining ACME/widget\n\n" +
		"  Checking branches...\n\n" +
		"    Checking branch develop\n      ‘Jenkinsfile’ not found\n    Does not meet criteria\n\n" +
		"    Checking branch main\n      ‘Jenkinsfile’ found\n    Met criteria\n" +
		"Changes detected: main (null → 2f9b79d)\nNo automatic build triggered for main\n\n" +
		"  2 branches were processed\n\n" +
		"Finished examining ACME/widget\n\nFinished: SUCCESS\n"
}

// scanServer serves one multibranch job whose indexing log is logText, and
// records every path requested.
func scanServer(t *testing.T, logText string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/job/team/job/svc/api/json":
			_, _ = w.Write([]byte(scanJobClass))
		case "/job/team/job/svc/indexing/logText/progressiveText":
			start, _ := strconv.Atoi(r.URL.Query().Get("start"))
			w.Header().Set("X-Text-Size", strconv.Itoa(len(logText)))
			if start < len(logText) {
				_, _ = w.Write([]byte(logText[start:]))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func runScanCmd(t *testing.T, logText string, args ...string) (string, error) {
	t.Helper()
	srv, _ := scanServer(t, logText)
	setupTestConfig(t, srv.URL)
	return executeCmd(t, append([]string{"scan", "team/svc"}, args...)...)
}

func TestScanPrintsLogVerbatim(t *testing.T) {
	log := scanLogFixture(time.Now().UTC())
	out, err := runScanCmd(t, log)
	require.NoError(t, err)

	// stdout is the log and nothing else: the header and the staleness note go
	// to stderr so a redirect keeps the log clean.
	assert.Equal(t, log, out)
}

func TestScanBranchFilterExtractsTheBlock(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--branch", "develop")
	require.NoError(t, err)

	assert.Equal(t, "    Checking branch develop\n      ‘Jenkinsfile’ not found\n    Does not meet criteria\n", out)
}

// --branch names a head in the log, so it must not be appended to the job path
// the way the global --branch is: that would ask a branch child for an indexing
// log it does not have.
func TestScanBranchFlagDoesNotTargetTheChildJob(t *testing.T) {
	srv, paths := scanServer(t, scanLogFixture(time.Now().UTC()))
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "scan", "team/svc", "--branch", "develop")
	require.NoError(t, err)

	for _, p := range *paths {
		assert.NotContains(t, p, "/job/develop")
	}
	assert.Contains(t, *paths, "/job/team/job/svc/indexing/logText/progressiveText")
}

// Trap 2: an absent head has three causes and the most common one is not
// rejection. The refusal must say so, and must carry the scan's age.
func TestScanBranchAbsentExplainsWhatAbsenceMeans(t *testing.T) {
	_, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--branch", "feature/new")
	require.Error(t, err)

	assert.Contains(t, err.Error(), "never mentions \"feature/new\"")
	assert.Contains(t, err.Error(), "pushed after this scan and has not been examined yet")
	assert.Contains(t, err.Error(), "does not exist on the remote")
	assert.Contains(t, err.Error(), "words its log differently")
	assert.Contains(t, err.Error(), "2 branches")
}

func TestScanBranchAbsentSuggestsSimilarNames(t *testing.T) {
	_, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--branch", "develo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Similar names in it: develop")
}

func TestScanSummaryTable(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "develop")
	assert.Contains(t, out, "not met")
	assert.Contains(t, out, "‘Jenkinsfile’ not found")
	assert.Contains(t, out, "main")
	assert.Contains(t, out, "met")
	assert.Contains(t, out, "2 heads parsed: 2 branches")
	// Trap 1: the table must say whose wording it was built from.
	assert.Contains(t, out, "Best effort")
	assert.Contains(t, out, "github-branch-source")
	assert.Contains(t, out, "The log's own totals agree")
}

// A head that met the criteria and still did not build says both things. The
// second line is the one a reader chasing a missing build needs, so it gets its
// own row instead of being truncated away.
func TestScanSummaryKeepsEveryActionLine(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "Changes detected: main")
	assert.Contains(t, out, "No automatic build triggered for main")
}

// Trap 1: a wording the parser does not know must not vanish into an empty
// cell. The line is printed verbatim and the head is counted as unread.
func TestScanSummarySurfacesUnknownWording(t *testing.T) {
	log := "[" + time.Now().UTC().Format("Mon Jan 02 15:04:05 MST 2006") + "] Starting branch indexing...\n" +
		"Examining ACME/widget\n\n" +
		"    Checking branch develop\n      Rejected: excluded by the branch filter\n\n" +
		"  1 branch were processed\n\nFinished: SUCCESS\n"

	out, err := runScanCmd(t, log, "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "Lines this parser does not recognize, verbatim")
	assert.Contains(t, out, "Rejected: excluded by the branch filter")
	assert.Contains(t, out, "1 head carries no verdict this parser knows")
	assert.Contains(t, out, "unrecognized")
}

// Trap 1: the log's own totals are the check on the parse. When they disagree
// the table must call itself incomplete rather than read as the whole story.
func TestScanSummaryWarnsWhenCountsDisagree(t *testing.T) {
	log := "[" + time.Now().UTC().Format("Mon Jan 02 15:04:05 MST 2006") + "] Starting branch indexing...\n" +
		"Examining ACME/widget\n\n" +
		"    Checking branch develop\n      ‘Jenkinsfile’ not found\n    Does not meet criteria\n\n" +
		"  7 branches were processed\n\nFinished: SUCCESS\n"

	out, err := runScanCmd(t, log, "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "INCOMPLETE")
	assert.Contains(t, out, "7 branches reported by the log, 1 parsed here")
}

// Trap 2: a scan old enough that the reader's branch may simply postdate it
// must warn, or the command answers a question it did not check.
func TestScanWarnsWhenScanIsStale(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC().Add(-72*time.Hour)), "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "warning: this scan is 3d0h old")
	assert.Contains(t, out, "not necessarily a head that was rejected")
}

func TestScanFreshScanStatesTheLimitWithoutWarning(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC().Add(-2*time.Hour)), "--summary")
	require.NoError(t, err)

	assert.Contains(t, out, "(2h0m ago)")
	assert.Contains(t, out, "Only the last scan is kept")
	assert.NotContains(t, out, "warning:")
}

// A scan with no "Finished:" line is still running: its absent heads mean even
// less than usual.
func TestScanSaysWhenTheScanIsStillRunning(t *testing.T) {
	log := "[" + time.Now().UTC().Format("Mon Jan 02 15:04:05 MST 2006") + "] Starting branch indexing...\n" +
		"Examining ACME/widget\n\n    Checking branch develop\n    Met criteria\n"

	out, err := runScanCmd(t, log, "--summary")
	require.NoError(t, err)
	assert.Contains(t, out, "has not finished")
	assert.Contains(t, out, "--follow")
}

func TestScanJSONCarriesTheParseAndItsCaveats(t *testing.T) {
	out, err := runScanCmd(t, scanLogFixture(time.Now().UTC()), "--json")
	require.NoError(t, err)

	var report struct {
		Job        string `json:"job"`
		Kind       string `json:"kind"`
		Stale      bool   `json:"stale"`
		BestEffort bool   `json:"bestEffort"`
		ParsedFrom string `json:"parsedFrom"`
		Result     string `json:"result"`
		Heads      []struct {
			Name     string   `json:"name"`
			Criteria string   `json:"criteria"`
			Lines    []string `json:"lines"`
		} `json:"heads"`
		Reported map[string]int `json:"reportedCounts"`
		Parsed   map[string]int `json:"parsedCounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	assert.Equal(t, "team/svc", report.Job)
	assert.Equal(t, "multibranch pipeline", report.Kind)
	assert.False(t, report.Stale)
	assert.True(t, report.BestEffort, "a consumer must see that the parse is a heuristic")
	assert.Contains(t, report.ParsedFrom, "github-branch-source")
	assert.Equal(t, "SUCCESS", report.Result)
	require.Len(t, report.Heads, 2)
	assert.Equal(t, "develop", report.Heads[0].Name)
	assert.Equal(t, "not met", report.Heads[0].Criteria)
	assert.Contains(t, report.Heads[0].Lines[0], "Checking branch develop")
	assert.Equal(t, report.Reported, report.Parsed)
}

func TestScanMaxBytesRefusesAHugeLog(t *testing.T) {
	_, err := runScanCmd(t, strings.Repeat("x", 5000), "--max-bytes", "1000")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to read it whole")
	assert.Contains(t, err.Error(), "--max-bytes 0")
}

// The command is meaningless on a job that indexes nothing, and the refusal has
// to name what to target instead.
func TestScanOnPlainJobNamesTheRightTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/json") {
			_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","jobs":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "scan", "team/legacy")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never indexes branches")
	assert.Contains(t, err.Error(), "jkit inspect team/legacy")
}
