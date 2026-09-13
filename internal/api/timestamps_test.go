package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stampServer records the query it was asked for and answers with body.
func stampServer(t *testing.T, body string, seen *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/timestamps/") {
			http.NotFound(w, r)
			return
		}
		if seen != nil {
			q := r.URL.Query()
			*seen = q
		}
		_, _ = fmt.Fprint(w, body)
	}))
}

func TestStampedLogLinesSplitsAndTrims(t *testing.T) {
	srv := stampServer(t, "00:00:01  first\r\n00:00:02  second\n", nil)
	defer srv.Close()

	var got []string
	err := NewClient(srv.URL, "u", "t").StampedLogLines("test", 42, TimestampElapsed, 0, 0, func(l string) bool {
		got = append(got, l)
		return true
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"00:00:01  first", "00:00:02  second"}, got)
}

// A line longer than any scanner token cap must arrive whole. bufio.Scanner
// would stop at 64 KB and report an error rather than the rest of the line.
func TestStampedLogLinesKeepsOversizeLineIntact(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	srv := stampServer(t, "00:00:01  "+long+"\n", nil)
	defer srv.Close()

	var got string
	err := NewClient(srv.URL, "u", "t").StampedLogLines("test", 42, TimestampElapsed, 0, 0, func(l string) bool {
		got = l
		return true
	})
	require.NoError(t, err)
	assert.Equal(t, len("00:00:01  ")+len(long), len(got))
}

func TestStampedLogLinesStopsWhenCallbackSaysSo(t *testing.T) {
	srv := stampServer(t, "a\nb\nc\n", nil)
	defer srv.Close()

	count := 0
	err := NewClient(srv.URL, "u", "t").StampedLogLines("test", 42, TimestampElapsed, 0, 0, func(string) bool {
		count++
		return false
	})
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestStampedLogLinesSendsLineWindowAndClock(t *testing.T) {
	tests := []struct {
		name             string
		format           TimestampFormat
		start, end       int
		wantKey, wantVal string
		wantStart        string
		wantEnd          string
	}{
		{"tail window", TimestampElapsed, -5, 0, "elapsed", elapsedPattern, "-5", ""},
		{"head window", TimestampWallClock, 1, 20, "time", wallClockPattern, "1", "20"},
		{"whole log", TimestampElapsed, 0, 0, "elapsed", elapsedPattern, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen url.Values
			srv := stampServer(t, "", &seen)
			defer srv.Close()

			err := NewClient(srv.URL, "u", "t").StampedLogLines("test", 42, tc.format, tc.start, tc.end, func(string) bool { return true })
			require.NoError(t, err)
			assert.Equal(t, tc.wantVal, seen.Get(tc.wantKey))
			assert.Equal(t, tc.wantStart, seen.Get("startLine"))
			assert.Equal(t, tc.wantEnd, seen.Get("endLine"))
			assert.True(t, seen.Has("appendLog"), "appendLog must be present or the log text is omitted")
		})
	}
}

// BuildLineTimes must not ask for the log body: fetching timestamps alone is
// what makes gap analysis cheaper than downloading the console.
func TestBuildLineTimesOmitsAppendLog(t *testing.T) {
	var seen url.Values
	srv := stampServer(t, "00001\n00109\n2667996\n", &seen)
	defer srv.Close()

	times, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 109, 2667996}, times)
	assert.False(t, seen.Has("appendLog"))
	assert.Equal(t, elapsedMillisPattern, seen.Get("elapsed"))
}

func TestBuildLineTimesRejectsNonNumericBody(t *testing.T) {
	srv := stampServer(t, "00001\nnot-a-number\n", nil)
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-number")
}

// /timestamps/ answers 404 both when the plugin is absent and when the build
// does not exist. The error has to say which.
func TestTimestampsNotFoundNamesTheAbsentPlugin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/timestamps/") {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"number":42,"result":"SUCCESS"}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamper plugin is not installed")
}

func TestTimestampsNotFoundFallsBackToMissingBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no build test #42", "the missing build is the cause, not the endpoint")
	assert.NotContains(t, err.Error(), "timestamps", "pointing at /timestamps/ names the wrong object")
}

// A console line that carried carriage-return fragments is stored with embedded
// newlines and only its first fragment gets a prefix, so a one-line window can
// come back as several physical lines. Callers must see all of them.
func TestStampedLogWindowReturnsEveryPhysicalLine(t *testing.T) {
	srv := stampServer(t, "00001  Dload Upload Total\n  0 0 0 --:--:--\n  0 0 0 --:--:--\n", nil)
	defer srv.Close()

	got, err := NewClient(srv.URL, "u", "t").StampedLogWindow("test", 42, 650, 650)
	require.NoError(t, err)
	assert.Len(t, got, 3, "a single log line rendered as three physical lines")
	assert.Equal(t, "00001  Dload Upload Total", got[0])
}

// The timestamp list is positional: entry N is console line N. A blank entry
// would shift every later line number, so it must be refused rather than
// skipped — a silently shifted index makes --slowest name the wrong command.
func TestBuildLineTimesRefusesABlankEntry(t *testing.T) {
	srv := stampServer(t, "00001\n\n00109\n", nil)
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "off by one")
}

// A trailing newline is not a blank entry: it is how the body normally ends.
func TestBuildLineTimesAcceptsATrailingNewline(t *testing.T) {
	srv := stampServer(t, "00001\n00109\n", nil)
	defer srv.Close()

	times, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 109}, times)
}

// A build with no timestamped output is not an error at this layer; the caller
// decides what too-few-lines means.
func TestBuildLineTimesAcceptsAnEmptyBody(t *testing.T) {
	srv := stampServer(t, "", nil)
	defer srv.Close()

	times, err := NewClient(srv.URL, "u", "t").BuildLineTimes("test", 42)
	require.NoError(t, err)
	assert.Empty(t, times)
}
