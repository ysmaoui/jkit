package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// revisionServer serves the two endpoints a diff needs: the history listing and
// the stored config behind each timestamp. A timestamp with no entry in configs
// gets the plugin's answer for one it does not hold, 200 and nothing.
func revisionServer(t *testing.T, history []jenkins.ConfigChange, configs map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobConfigHistory/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"jobConfigHistory": history})
		case strings.HasSuffix(r.URL.Path, "/jobConfigHistory/configOutput"):
			ts := r.URL.Query().Get("timestamp")
			fetched = append(fetched, ts)
			_, _ = w.Write([]byte(configs[ts]))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &fetched
}

const (
	revisionOld = "<flow-definition>\n  <numToKeep>10</numToKeep>\n  <disabled>false</disabled>\n</flow-definition>\n"
	revisionNew = "<flow-definition>\n  <numToKeep>50</numToKeep>\n  <disabled>false</disabled>\n</flow-definition>\n"
)

func twoRevisions() ([]jenkins.ConfigChange, map[string]string) {
	return []jenkins.ConfigChange{
			change("2026-08-27_14-58-13", "Changed", "Ada Lovelace"),
			change("2026-07-24_13-06-30", "Changed", "SYSTEM"),
		}, map[string]string{
			"2026-07-24_13-06-30": revisionOld,
			"2026-08-27_14-58-13": revisionNew,
		}
}

func TestInspectDiffComparesTheTwoMostRecentRevisions(t *testing.T) {
	history, configs := twoRevisions()
	history = append(history, change("2026-01-01_00-00-00", "Created", "SYSTEM"))
	srv, fetched := revisionServer(t, history, configs)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "inspect", "team/svc", "--diff")
	require.NoError(t, err)

	assert.Equal(t, []string{"2026-07-24_13-06-30", "2026-08-27_14-58-13"}, *fetched,
		"--diff on its own compares the newest pair, oldest fetched first")
	assert.Contains(t, out, "--- team/svc @ 2026-07-24_13-06-30")
	assert.Contains(t, out, "+++ team/svc @ 2026-08-27_14-58-13")
	assert.Contains(t, out, "@@ -1,4 +1,4 @@")
	assert.Contains(t, out, "-  <numToKeep>10</numToKeep>")
	assert.Contains(t, out, "+  <numToKeep>50</numToKeep>")
	assert.Contains(t, out, " </flow-definition>", "context lines keep the reader oriented in the XML")
}

// Trap 3 of jk-2gc.6: the plugin's doDiffFiles swaps its arguments so the older
// revision is always the left side. A pair given the other way round has to
// read the same here as it does in the Jenkins UI.
func TestInspectDiffPutsTheOlderRevisionFirst(t *testing.T) {
	history, configs := twoRevisions()
	srv, fetched := revisionServer(t, history, configs)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "inspect", "team/svc", "--diff",
		"--diff-from", "2026-08-27_14-58-13", "--diff-to", "2026-07-24_13-06-30")
	require.NoError(t, err)

	assert.Equal(t, []string{"2026-07-24_13-06-30", "2026-08-27_14-58-13"}, *fetched)
	assert.Contains(t, out, "--- team/svc @ 2026-07-24_13-06-30")
	assert.Contains(t, out, "+++ team/svc @ 2026-08-27_14-58-13")
	assert.Contains(t, out, "-  <numToKeep>10</numToKeep>")
}

// With both timestamps given there is nothing to look up, so the listing is not
// requested at all.
func TestInspectDiffWithBothTimestampsSkipsTheHistoryListing(t *testing.T) {
	history, configs := twoRevisions()
	var listed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobConfigHistory/api/json") {
			listed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"jobConfigHistory": history})
			return
		}
		_, _ = w.Write([]byte(configs[r.URL.Query().Get("timestamp")]))
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "inspect", "team/svc", "--diff",
		"--diff-from", "2026-07-24_13-06-30", "--diff-to", "2026-08-27_14-58-13")
	require.NoError(t, err)
	assert.False(t, listed)
}

// Trap 1 of jk-2gc.6, at the command level: a timestamp the plugin does not
// hold answers 200 with nothing, which must not surface as an empty diff.
func TestInspectDiffNamesARejectedTimestamp(t *testing.T) {
	history, configs := twoRevisions()
	srv, _ := revisionServer(t, history, configs)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "inspect", "team/svc", "--diff",
		"--diff-from", "1999-01-01_00-00-00", "--diff-to", "2026-08-27_14-58-13")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1999-01-01_00-00-00")
	assert.Contains(t, err.Error(), "no stored configuration")
}

// A malformed timestamp is caught before any request: the endpoint would answer
// 200 and an empty body for it, which says nothing about what was wrong.
func TestInspectDiffRejectsAMalformedTimestamp(t *testing.T) {
	history, configs := twoRevisions()
	srv, fetched := revisionServer(t, history, configs)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "inspect", "team/svc", "--diff",
		"--diff-from", "notadate", "--diff-to", "2026-08-27_14-58-13")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"notadate"`)
	assert.Contains(t, err.Error(), "2006-01-02_15-04-05")
	assert.Empty(t, *fetched, "a malformed timestamp is not worth a request")
}

// The folder case: a job the plugin has recorded once cannot be diffed, and
// saying so beats a diff of a revision against itself.
func TestInspectDiffOnASingleRevision(t *testing.T) {
	srv, _ := revisionServer(t,
		[]jenkins.ConfigChange{change("2026-07-24_13-06-30", "Created", "SYSTEM")},
		map[string]string{"2026-07-24_13-06-30": revisionOld})
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "inspect", "team/svc", "--diff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one recorded revision")
	assert.Contains(t, err.Error(), "2026-07-24 13:06:30")
	assert.Contains(t, err.Error(), "a diff needs two")
}

// An empty listing is ambiguous the same way --history is: without the
// permission the plugin returns an empty list rather than refusing.
func TestInspectDiffWithNoHistoryNamesThePermission(t *testing.T) {
	srv, _ := revisionServer(t, nil, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "inspect", "team/svc", "--diff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/ExtendedRead")
}

func TestInspectDiffJSONEmitsHunks(t *testing.T) {
	history, configs := twoRevisions()
	srv, _ := revisionServer(t, history, configs)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "inspect", "team/svc", "--diff", "--json")
	require.NoError(t, err)

	var got jenkins.ConfigRevisionDiff
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "team/svc", got.Job)
	assert.Equal(t, "2026-07-24_13-06-30", got.From)
	assert.Equal(t, "2026-08-27_14-58-13", got.To)
	assert.False(t, got.MaskedOnly)
	require.Len(t, got.Hunks, 1)
	assert.Equal(t, 1, got.Hunks[0].OldStart)
	assert.Contains(t, got.Hunks[0].Lines, jenkins.DiffLine{Op: "+", Text: "  <numToKeep>50</numToKeep>"})
}

// Two revisions with the same bytes are the normal answer for a job whose
// history is nothing but re-indexing writes. stdout stays empty so a diff can
// be piped; the explanation goes to stderr.
func TestPrintConfigDiffReportsNoChangeOnStderr(t *testing.T) {
	var out, warn bytes.Buffer
	d := jenkins.DiffConfigRevisions("team/svc", "2026-07-24_13-06-30", "2026-08-27_14-58-13",
		[]byte(revisionOld), []byte(revisionOld))
	require.NoError(t, printConfigDiff(&out, &warn, d))

	assert.Empty(t, out.String())
	assert.Contains(t, warn.String(), "No change: team/svc stored the same config.xml")
	assert.Contains(t, warn.String(), "2026-07-24_13-06-30")
}

// Trap 2 of jk-2gc.6: a diff whose every change is a value Jenkins hid is not
// evidence that the job was reconfigured, and has to say so.
func TestPrintConfigDiffCallsOutAMaskedOnlyChange(t *testing.T) {
	var out, warn bytes.Buffer
	d := jenkins.DiffConfigRevisions("team/svc", "a", "b",
		[]byte("<authToken>{AQAAABAAAAAgKGFrZQ==}</authToken>\n"),
		[]byte("<authToken>********</authToken>\n"))
	require.NoError(t, printConfigDiff(&out, &warn, d))

	assert.Contains(t, out.String(), "+<authToken>********</authToken>")
	assert.Contains(t, warn.String(), "identical apart from values Jenkins hides")
	assert.Contains(t, warn.String(), "not evidence that the job was reconfigured")
}

// A real change must not be explained away as masking.
func TestPrintConfigDiffStaysQuietOnARealChange(t *testing.T) {
	var out, warn bytes.Buffer
	d := jenkins.DiffConfigRevisions("team/svc", "a", "b", []byte(revisionOld), []byte(revisionNew))
	require.NoError(t, printConfigDiff(&out, &warn, d))

	assert.Contains(t, out.String(), "-  <numToKeep>10</numToKeep>")
	assert.Empty(t, warn.String())
}

// config.xml carries whatever someone typed into a description field, escape
// sequences included, and the diff is a rendered view rather than the byte-exact
// dump --xml gives.
func TestPrintConfigDiffStripsControlCharacters(t *testing.T) {
	var out, warn bytes.Buffer
	d := jenkins.DiffConfigRevisions("team/svc", "a", "b",
		[]byte("<description>plain</description>\n"),
		[]byte("<description>\x1b[2Kwiped</description>\n"))
	require.NoError(t, printConfigDiff(&out, &warn, d))

	assert.Contains(t, out.String(), "+<description>wiped</description>")
	assert.NotContains(t, out.String(), "\x1b")
}

// A mode flag accepted and then ignored is what the jk-2gc.5 checkpoint set out
// to stop, and --diff adds three more flags to the matrix.
func TestInspectDiffRejectsMeaninglessFlagCombinations(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"diff with xml":           {[]string{"--diff", "--xml"}, "none of the others can be"},
		"diff with history":       {[]string{"--diff", "--history"}, "none of the others can be"},
		"diff with show-system":   {[]string{"--diff", "--show-system"}, "none of the others can be"},
		"diff with show-secrets":  {[]string{"--diff", "--show-secrets"}, "none of the others can be"},
		"timestamps without diff": {[]string{"--diff-from", "2026-07-24_13-06-30", "--diff-to", "2026-08-27_14-58-13"}, "only apply to --diff"},
		"from without to":         {[]string{"--diff", "--diff-from", "2026-07-24_13-06-30"}, "pass both, or neither"},
		"to without from":         {[]string{"--diff", "--diff-to", "2026-08-27_14-58-13"}, "pass both, or neither"},
		"timestamps with history": {[]string{"--history", "--diff-from", "2026-07-24_13-06-30"}, "only apply to --diff"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setupTestConfig(t, "https://jenkins.example.com")
			_, err := executeCmd(t, append([]string{"inspect", "team/svc"}, tt.args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
