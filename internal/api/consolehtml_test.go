package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// htmlAnswer is one progressiveHtml answer a fake server gives for a start.
type htmlAnswer struct {
	body      string
	size      int64
	more      bool
	annotator string
}

func consoleHTMLServer(t *testing.T, version string, answers map[string]htmlAnswer, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("start")
		*seen = append(*seen, start+" "+r.Header.Get("X-ConsoleAnnotator"))
		a, ok := answers[start]
		if r.URL.Path != "/job/app/7/logText/progressiveHtml" || !ok {
			t.Errorf("unexpected request %s start %s", r.URL.Path, start)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Jenkins", version)
		w.Header().Set("X-Text-Size", strconv.FormatInt(a.size, 10))
		if a.more {
			w.Header().Set("X-More-Data", "true")
		}
		if a.annotator != "" {
			w.Header().Set("X-ConsoleAnnotator", a.annotator)
		}
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCopyConsoleHTMLComplete(t *testing.T) {
	var seen []string
	body := "<span class=\"pipeline-node-3\">a\r\nb\r\n</span>50%\r60%\r\n"
	srv := consoleHTMLServer(t, "2.541", map[string]htmlAnswer{"0": {body: body, size: 99}}, &seen)
	var out strings.Builder
	n, partial, err := NewClient(srv.URL, "u", "t").CopyConsoleHTML(context.Background(), "app", 7, &out)
	require.NoError(t, err)
	assert.False(t, partial)
	assert.Equal(t, "<span class=\"pipeline-node-3\">a\nb\n</span>50%\r60%\n", out.String())
	assert.Equal(t, int64(out.Len()), n)
	assert.Equal(t, []string{"0 "}, seen)
}

// A running build's answer that stops short of the line cap is all there is.
func TestCopyConsoleHTMLRunningUncapped(t *testing.T) {
	var seen []string
	srv := consoleHTMLServer(t, "2.541", map[string]htmlAnswer{"0": {body: "a\r\n", size: 3, more: true, annotator: "x"}}, &seen)
	var out strings.Builder
	_, partial, err := NewClient(srv.URL, "u", "t").CopyConsoleHTML(context.Background(), "app", 7, &out)
	require.NoError(t, err)
	assert.False(t, partial)
	assert.Equal(t, "a\n", out.String())
	assert.Len(t, seen, 1)
}

func capped(prefix string) string {
	return prefix + strings.Repeat("l\r\n", maxLinesRead)
}

// Up to 2.508 X-Text-Size is where the answer stopped, so the copy goes on
// from there with the annotator state.
func TestCopyConsoleHTMLFollowsCountedSize(t *testing.T) {
	var seen []string
	srv := consoleHTMLServer(t, "2.507", map[string]htmlAnswer{
		"0":   {body: capped("<span>"), size: 100, more: true, annotator: "s1"},
		"100": {body: "tail\r\n</span>", size: 105, more: true, annotator: "s2"},
	}, &seen)
	var out strings.Builder
	_, partial, err := NewClient(srv.URL, "u", "t").CopyConsoleHTML(context.Background(), "app", 7, &out)
	require.NoError(t, err)
	assert.False(t, partial)
	assert.Equal(t, []string{"0 ", "100 s1"}, seen)
	assert.True(t, strings.HasSuffix(out.String(), "l\ntail\n</span>"))
}

// From 2.509 X-Text-Size is the stored length, past where a capped answer
// stopped, so going on from it would skip text.
func TestCopyConsoleHTMLStopsAtCapOnStoredSize(t *testing.T) {
	var seen []string
	srv := consoleHTMLServer(t, "2.541", map[string]htmlAnswer{
		"0": {body: capped(""), size: 1 << 20, more: true, annotator: "s1"},
	}, &seen)
	var out strings.Builder
	_, partial, err := NewClient(srv.URL, "u", "t").CopyConsoleHTML(context.Background(), "app", 7, &out)
	require.NoError(t, err)
	assert.True(t, partial)
	assert.Len(t, seen, 1)
}

func TestCopyConsoleHTMLNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, _, err := NewClient(srv.URL, "u", "t").CopyConsoleHTML(context.Background(), "app", 7, &strings.Builder{})
	var nfe *jenkins.NotFoundError
	require.ErrorAs(t, err, &nfe)
}
