package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
)

// stampedLogServer serves the timestamper endpoint, honouring startLine/endLine
// the way Jenkins does (1-based, negative counts back from the end) so the tests
// exercise the real window arithmetic. It records the last query seen.
func stampedLogServer(t *testing.T, lines []string, seen *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timestamps/"):
			q := r.URL.Query()
			if seen != nil {
				*seen = q
			}
			start, end := 1, len(lines)
			if v := q.Get("startLine"); v != "" {
				_, _ = fmt.Sscanf(v, "%d", &start)
				if start < 0 {
					start = len(lines) + start + 1
				}
			}
			if v := q.Get("endLine"); v != "" {
				_, _ = fmt.Sscanf(v, "%d", &end)
			}
			for i := start; i <= end && i <= len(lines); i++ {
				if i >= 1 {
					_, _ = fmt.Fprintf(w, "%s\n", lines[i-1])
				}
			}
		case strings.Contains(r.URL.Path, "logText"):
			w.Header().Set("X-Text-Size", "100")
		case strings.Contains(r.URL.Path, "/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5, "result": "SUCCESS", "building": false,
				"builds": []map[string]any{{"number": 5, "result": "SUCCESS", "building": false}},
			})
		}
	}))
}

var stampedLines = []string{
	"00:00:01  one",
	"00:00:02  two",
	"00:00:03  three ERROR",
	"00:00:04  four",
	"00:00:05  five ERROR",
}

func TestLogElapsedTailUsesServerWindow(t *testing.T) {
	var seen url.Values
	srv := stampedLogServer(t, stampedLines, &seen)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--elapsed", "--tail", "2")
	require.NoError(t, err)
	assert.Equal(t, "-2", seen.Get("startLine"), "--tail must be served by the endpoint, not by downloading the log")
	assert.Contains(t, out, "00:00:04  four")
	assert.Contains(t, out, "00:00:05  five")
	assert.NotContains(t, out, "one")
}

func TestLogTimestampsHeadUsesServerWindow(t *testing.T) {
	var seen url.Values
	srv := stampedLogServer(t, stampedLines, &seen)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--timestamps", "--head", "2")
	require.NoError(t, err)
	assert.Equal(t, "1", seen.Get("startLine"))
	assert.Equal(t, "2", seen.Get("endLine"))
	assert.Equal(t, "time", firstClockKey(seen))
	assert.Contains(t, out, "one")
	assert.NotContains(t, out, "three")
}

func TestLogElapsedGrepMatchesTheWholePrintedLine(t *testing.T) {
	srv := stampedLogServer(t, stampedLines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--elapsed", "--grep", "ERROR")
	require.NoError(t, err)
	assert.Contains(t, out, "three ERROR")
	assert.Contains(t, out, "five ERROR")
	assert.NotContains(t, out, "four")

	// The timestamp prefix is part of what is printed, so it is searchable too.
	out, err = executeCmd(t, "log", "my-app", "5", "--elapsed", "--grep", "00:00:02")
	require.NoError(t, err)
	assert.Contains(t, out, "two")
	assert.NotContains(t, out, "three")
}

func TestLogElapsedGrepHonoursTailAndHead(t *testing.T) {
	srv := stampedLogServer(t, stampedLines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--elapsed", "--grep", "ERROR", "--tail", "1")
	require.NoError(t, err)
	assert.Contains(t, out, "five ERROR")
	assert.NotContains(t, out, "three ERROR")
}

func TestLogStampRefusals(t *testing.T) {
	srv := stampedLogServer(t, stampedLines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"both clocks", []string{"--timestamps", "--elapsed"}, "pick one clock"},
		{"with follow", []string{"--elapsed", "-f"}, "no end-of-stream signal"},
		{"with stage", []string{"--elapsed", "--stage", "Build"}, "carries no timestamps"},
		{"with stage-id", []string{"--timestamps", "--stage-id", "7"}, "carries no timestamps"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeCmd(t, append([]string{"log", "my-app", "5"}, tc.args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// An unfiltered stamped dump is bounded by --max-bytes even though the endpoint
// reports no size of its own.
func TestLogStampedRespectsMaxBytes(t *testing.T) {
	srv := stampedLogServer(t, stampedLines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--elapsed", "--max-bytes", "10")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before timestamps are added")

	_, err = executeCmd(t, "log", "my-app", "5", "--elapsed", "--max-bytes", "0")
	require.NoError(t, err)
}

func firstClockKey(q url.Values) string {
	for _, k := range []string{"time", "elapsed"} {
		if q.Has(k) {
			return k
		}
	}
	return ""
}

// --head is bounded by the endpoint's endLine, never by counting what comes
// back: one log line can arrive as several physical lines, so counting them
// would stop short of the requested line. Regression for exactly that.
func TestLogHeadCountsLogLinesNotPhysicalLines(t *testing.T) {
	lines := []string{
		"00:00:01  one",
		"00:00:02  progress\n  fragment a\n  fragment b",
		"00:00:03  three",
	}
	srv := stampedLogServer(t, lines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--elapsed", "--head", "3")
	require.NoError(t, err)
	assert.Contains(t, out, "three", "the third log line must survive its predecessor's embedded newlines")
	assert.Contains(t, out, "fragment b")
}

func TestFirstFragmentMarksWhatItLeftOut(t *testing.T) {
	assert.Equal(t, "", firstFragment(nil))
	assert.Equal(t, "one", firstFragment([]string{"00:00:01  one"}))
	assert.Equal(t, "one …", firstFragment([]string{"00:00:01  one", "  frag"}))
}

func TestLargestGapsPicksTheLineThatStartedTheWait(t *testing.T) {
	// Lines 1..4 at 0s, 1s, 61s, 62s: the 60s wait began on line 2.
	gaps := largestGaps([]int64{0, 1000, 61000, 62000}, 2)
	require.Len(t, gaps, 2)
	assert.Equal(t, 2, gaps[0].Line)
	assert.Equal(t, int64(60000), gaps[0].GapMillis)
	assert.Equal(t, int64(1000), gaps[0].ElapsedMillis, "AT is when the wait started")
	assert.Equal(t, int64(1000), gaps[1].GapMillis)
}

func TestLargestGapsNeedsTwoLines(t *testing.T) {
	assert.Nil(t, largestGaps([]int64{5}, 3))
	assert.Nil(t, largestGaps([]int64{1, 2}, 0))
}

func TestFormatGapKeepsSubSecondResolution(t *testing.T) {
	assert.Equal(t, "999ms", formatGap(999))
	assert.Equal(t, "1s", formatGap(1000))
	assert.Equal(t, "2m36s", formatGap(156510))
}

func TestSlowestRefusals(t *testing.T) {
	srv := stampedLogServer(t, stampedLines, nil)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"stage", []string{"--slowest", "3", "--stage", "Build"}, "carry no stage"},
		{"stage-id", []string{"--slowest", "3", "--stage-id", "7"}, "carry no stage"},
		{"grep", []string{"--slowest", "3", "--grep", "x"}, "invent gaps"},
		{"tail", []string{"--slowest", "3", "--tail", "2"}, "already reports only the N largest"},
		{"head", []string{"--slowest", "3", "--head", "2"}, "already reports only the N largest"},
		{"elapsed", []string{"--slowest", "3", "--elapsed"}, "already reports each gap"},
		{"follow", []string{"--slowest", "3", "-f"}, "one-shot ranking"},
		{"negative", []string{"--slowest", "-1"}, "positive count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeCmd(t, append([]string{"log", "my-app", "5"}, tc.args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestSlowestReportsGapsFromTheLiveShapeOfTheEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timestamps/"):
			q := r.URL.Query()
			if !q.Has("appendLog") {
				// Timestamps only: one elapsed value per log line.
				_, _ = fmt.Fprint(w, "00000\n01000\n61000\n62000\n")
				return
			}
			_, _ = fmt.Fprintf(w, "%s  line %s\n", q.Get("startLine"), q.Get("startLine"))
		case strings.Contains(r.URL.Path, "/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "result": "SUCCESS"})
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--slowest", "1")
	require.NoError(t, err)
	assert.Contains(t, out, "1m0s")
	assert.Contains(t, out, "line 2", "the gap is attributed to the line that started the wait")
}

func TestSlowestNeedsTimestampedLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "result": "SUCCESS"})
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--slowest", "3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too few to measure a gap")
}

func TestSlowestNotesAnUnfinishedBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timestamps/"):
			if !r.URL.Query().Has("appendLog") {
				_, _ = fmt.Fprint(w, "00000\n01000\n61000\n")
				return
			}
			_, _ = fmt.Fprint(w, "00000  still going\n")
		case strings.Contains(r.URL.Path, "/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": true})
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out, notes bytes.Buffer
	client := api.NewClient(srv.URL, "u", "t")
	require.NoError(t, runSlowest(client, "my-app", 5, 1, false, "", &out, &notes))
	assert.Contains(t, notes.String(), "still running")
}
