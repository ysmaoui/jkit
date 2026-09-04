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

func change(date, operation, user string) jenkins.ConfigChange {
	id := user
	if user == "Ada Lovelace" {
		id = "ada"
	}
	return jenkins.ConfigChange{Date: date, Operation: operation, User: user, UserID: id}
}

func renderHistory(t *testing.T, entries []jenkins.ConfigChange, showSystem bool) (string, string) {
	t.Helper()
	var out, warn bytes.Buffer
	require.NoError(t, printConfigHistory(&out, &warn, "team/svc", entries, showSystem))
	return out.String(), warn.String()
}

// A multibranch branch job records nothing but re-indexing: pairs of SYSTEM
// writes one second apart. Listing them raw buries whatever a person changed.
func TestInspectHistoryCollapsesSystemChurn(t *testing.T) {
	entries := []jenkins.ConfigChange{
		change("2026-08-27_14-58-13", "Changed", "SYSTEM"),
		change("2026-08-27_14-58-12", "Changed", "SYSTEM"),
		change("2026-08-20_10-00-00", "Changed", "Ada Lovelace"),
		change("2026-08-14_14-58-13", "Changed", "SYSTEM"),
		change("2026-08-14_14-58-12", "Changed", "SYSTEM"),
	}

	out, _ := renderHistory(t, entries, false)
	assert.Contains(t, out, "Ada Lovelace")
	assert.Contains(t, out, "2 writes")
	assert.Contains(t, out, "2026-08-14 14:58:12 → 2026-08-14 14:58:13")
	assert.Equal(t, 1, strings.Count(out, "2026-08-27 14:58:13"), "a collapsed run prints its range once")
	assert.Contains(t, out, "4 of 5 entries are automated SYSTEM writes")
	assert.Contains(t, out, "--show-system")

	shown, _ := renderHistory(t, entries, true)
	for _, e := range entries {
		assert.Contains(t, shown, e.Timestamp(), "--show-system lists every entry")
	}
	assert.NotContains(t, shown, "writes")
	assert.NotContains(t, shown, "→")
}

// A lone automated write is the job's creation, not churn: folding one entry
// into a summary row would hide the only thing the history has to say.
func TestInspectHistoryKeepsASingleSystemEntry(t *testing.T) {
	out, _ := renderHistory(t, []jenkins.ConfigChange{change("2026-07-24_09-56-57", "Created", "SYSTEM")}, false)
	assert.Contains(t, out, "2026-07-24 09:56:57")
	assert.Contains(t, out, "Created")
	assert.Contains(t, out, "SYSTEM")
	assert.NotContains(t, out, "1 writes")
	assert.NotContains(t, out, "collapsed")
}

// The plugin resolves the operation from its message bundle at write time and
// stores the resolved text, so a Japanese controller writes 変更. Rendering and
// collapsing must both survive that: authorship comes from userID.
func TestInspectHistoryHandlesLocalizedOperation(t *testing.T) {
	entries := []jenkins.ConfigChange{
		change("2026-08-27_14-58-13", "変更", "SYSTEM"),
		change("2026-08-27_14-58-12", "変更", "SYSTEM"),
		change("2026-08-20_10-00-00", "変更", "Ada Lovelace"),
	}
	out, _ := renderHistory(t, entries, false)
	assert.Contains(t, out, "変更")
	assert.Contains(t, out, "Ada Lovelace")
	assert.Contains(t, out, "2 writes", "the SYSTEM run collapses whatever language it is written in")
}

// The number of entries is what the server still retains, not a change count:
// maxHistoryEntries caps it and maxEntriesPerPage can truncate the response
// with no marker.
func TestInspectHistoryCallsTheCountRetentionLimited(t *testing.T) {
	out, _ := renderHistory(t, []jenkins.ConfigChange{change("2026-08-20_10-00-00", "Changed", "Ada Lovelace")}, false)
	assert.Contains(t, out, "Retained entries: 1")
	assert.Contains(t, out, "not every change ever made")
}

// Without Job/Configure the plugin answers 200 with an empty array, so an empty
// result cannot be reported as "nothing changed" on its own.
func TestInspectHistoryEmptyNamesThePermission(t *testing.T) {
	out, warn := renderHistory(t, nil, false)
	assert.Empty(t, out)
	assert.Contains(t, warn, "No config history for team/svc")
	assert.Contains(t, warn, "Job/Configure")
	assert.Contains(t, warn, "empty list")
}

// Names come back percent-encoded inside the JSON string; printing them raw
// shows a branch as "feature%2Fbuild".
func TestInspectHistoryDecodesEncodedNames(t *testing.T) {
	entries := []jenkins.ConfigChange{{
		Date:        "2026-08-20_10-00-00",
		Operation:   "Renamed",
		User:        "Ada Lovelace",
		UserID:      "ada",
		Job:         "team/svc/feature%2Fbuild",
		OldName:     "feature%2Fold",
		CurrentName: "feature%2Fnew",
	}}
	out, _ := renderHistory(t, entries, false)
	assert.Contains(t, out, "renamed feature/old → feature/new")
	assert.NotContains(t, out, "%2F")
}

func TestInspectHistoryShowsChangeReason(t *testing.T) {
	reason := "bumped the timeout"
	entries := []jenkins.ConfigChange{{
		Date: "2026-08-20_10-00-00", Operation: "Changed", User: "Ada Lovelace", UserID: "ada", Comment: &reason,
	}}
	out, _ := renderHistory(t, entries, false)
	assert.Contains(t, out, reason)
}

func historyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/jobConfigHistory/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
}

const historyBody = `{"jobConfigHistory":[
 {"changeReasonComment":null,"currentName":"","date":"2026-08-27_14-58-13","hasConfig":true,
  "job":"team/svc","oldName":"","operation":"Changed","user":"SYSTEM","userID":"SYSTEM"},
 {"changeReasonComment":null,"currentName":"","date":"2026-08-27_14-58-12","hasConfig":true,
  "job":"team/svc","oldName":"","operation":"Changed","user":"SYSTEM","userID":"SYSTEM"},
 {"changeReasonComment":null,"currentName":"","date":"2026-08-20_10-00-00","hasConfig":true,
  "job":"team/svc","oldName":"","operation":"Changed","user":"Ada Lovelace","userID":"ada"}]}`

func TestInspectHistoryCommand(t *testing.T) {
	srv := historyServer(t, historyBody)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "inspect", "team/svc", "--history")
	require.NoError(t, err)
	assert.Contains(t, out, "WHEN")
	assert.Contains(t, out, "Ada Lovelace")
	assert.Contains(t, out, "2 writes")
	// --history reads the change log, never config.xml.
	assert.NotContains(t, out, "Pipeline script")
}

// --json is the machine-readable view, so it carries every entry: collapsing is
// a reading aid for the table only.
func TestInspectHistoryCommandJSON(t *testing.T) {
	srv := historyServer(t, historyBody)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "inspect", "team/svc", "--history", "--json")
	require.NoError(t, err)

	var got []struct {
		Date      string `json:"date"`
		Operation string `json:"operation"`
		User      string `json:"user"`
		UserID    string `json:"userID"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 3)
	assert.Equal(t, "2026-08-27_14-58-13", got[0].Date)
	assert.Equal(t, "ada", got[2].UserID)
}
