package export

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
)

// scriptedIDs are the steps of the done-scripted and mid-scripted runs.
var scriptedIDs = []string{"8", "9", "14", "21", "24", "35", "42", "46", "36", "41", "53"}

// annotate renders logs as progressiveHtml does with file log storage: each
// node's text in its pipeline-node span, [Pipeline] lines outside any, and
// the logs a line at a time in turn, as parallel branches interleave.
func annotate(logs map[string]string, ids []string) string {
	var b strings.Builder
	b.WriteString("Started by user admin\n")
	lines := map[string][]string{}
	for _, id := range ids {
		lines[id] = slices.Collect(strings.Lines(logs[id]))
		b.WriteString("[Pipeline] sh\n")
	}
	for more := true; more; {
		more = false
		for _, id := range ids {
			if len(lines[id]) == 0 {
				continue
			}
			more = true
			fmt.Fprintf(&b, `<span class="pipeline-node-%s">%s</span>`, id, html.EscapeString(lines[id][0]))
			lines[id] = lines[id][1:]
		}
	}
	b.WriteString("[Pipeline] End of Pipeline\nFinished: FAILURE\n")
	return b.String()
}

// consoleRun is scriptedRun with an annotated console, escaped characters in
// a log and a step that writes nothing.
func consoleRun(t *testing.T) *fakeRun {
	t.Helper()
	run := scriptedRun(t)
	delete(run.failLog, "24")
	delete(run.stepLogs, "41")
	run.stepLogs["8"] = "a < b && c > \"d\" 'e'\n" + run.stepLogs["8"]
	run.consoleHTML = annotate(run.stepLogs, scriptedIDs)
	return run
}

func renderPage(t *testing.T, client *api.Client, job string, opts Options) string {
	t.Helper()
	snap, err := collect(t, client, job, opts)
	require.NoError(t, err)
	snap.ExportedAtMillis = 0
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, snap))
	return buf.String()
}

func TestCollectConsoleMatchesSteps(t *testing.T) {
	for _, maxBytes := range []int64{0, 300} {
		t.Run(fmt.Sprint(maxBytes), func(t *testing.T) {
			run := consoleRun(t)
			client := run.serve(t)
			want := renderPage(t, client, run.job, Options{LogSource: LogSourceSteps, Concurrency: 4, MaxLogBytes: maxBytes})
			run.stepLogHits.Store(0)

			var notices []string
			got := renderPage(t, client, run.job, Options{LogSource: LogSourceConsole, Concurrency: 4, MaxLogBytes: maxBytes,
				Notice: func(msg string) { notices = append(notices, msg) }})
			assert.Equal(t, want, got)
			assert.Equal(t, []string{"reading step logs from the annotated console"}, notices)
			assert.Zero(t, run.stepLogHits.Load(), "no step log is read on its own")
			assert.Equal(t, int32(1), run.htmlHits.Load())
		})
	}
}

func TestCollectAutoLogSource(t *testing.T) {
	done := consoleRun(t)
	_, err := collect(t, done.serve(t), done.job, Options{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), done.htmlHits.Load(), "a finished run reads the annotated console")
	assert.Zero(t, done.stepLogHits.Load())

	mid := loadRun(t, "mid", "scripted")
	mid.consoleHTML = annotate(map[string]string{"8": "x\n"}, []string{"8"})
	_, err = collect(t, mid.serve(t), mid.job, Options{})
	require.NoError(t, err)
	assert.Zero(t, mid.htmlHits.Load(), "a running run is read step by step")
}

func TestCollectConsoleFallsBack(t *testing.T) {
	tests := map[string]struct {
		set     func(*fakeRun)
		timeout time.Duration
		reason  string
	}{
		"no node spans": {
			set:    func(r *fakeRun) { r.consoleHTML = "Started by user admin\nFinished: FAILURE\n" },
			reason: "no pipeline node spans",
		},
		"stream error": {
			set:    func(r *fakeRun) { r.failLog["html"] = http.StatusInternalServerError },
			reason: "500",
		},
		"partial": {
			set: func(r *fakeRun) {
				r.consoleHTML = strings.Repeat("[Pipeline] echo\n", 10000) + r.consoleHTML
				r.consoleHTMLMore = true
			},
			reason: "may stop short",
		},
		"timeout": {
			set:     func(r *fakeRun) { r.stall["html"] = true },
			timeout: 300 * time.Millisecond,
			reason:  "--timeout",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			run := consoleRun(t)
			tc.set(run)
			var opts []api.ClientOption
			if tc.timeout > 0 {
				opts = append(opts, api.WithTimeout(tc.timeout))
			}
			client := run.serve(t, opts...)
			want := renderPage(t, client, run.job, Options{LogSource: LogSourceSteps, Concurrency: 4})
			run.stepLogHits.Store(0)

			var notices []string
			got := renderPage(t, client, run.job, Options{LogSource: LogSourceConsole, Concurrency: 4,
				Notice: func(msg string) { notices = append(notices, msg) }})
			assert.Equal(t, want, got)
			require.Len(t, notices, 2)
			assert.Contains(t, notices[1], "reading step logs one by one")
			assert.Contains(t, notices[1], tc.reason)
			assert.Positive(t, run.stepLogHits.Load(), "steps read one by one")
		})
	}
}

func TestCollectConsoleAuthFailureStops(t *testing.T) {
	run := consoleRun(t)
	run.failLog["html"] = http.StatusUnauthorized
	dir := t.TempDir()
	snap, err := Collect(context.Background(), run.serve(t), run.job, 1, Options{LogSource: LogSourceConsole, SpoolDir: dir})
	require.Error(t, err)
	assert.Nil(t, snap)
	assert.Contains(t, err.Error(), "auth login")
	assert.Zero(t, run.stepLogHits.Load())
	assert.Zero(t, openSpools(t, dir))
}
