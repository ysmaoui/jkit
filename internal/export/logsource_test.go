package export

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/staplertest"
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
	assert.Equal(t, int32(1), mid.htmlHits.Load(), "a running run reads the annotated console too")
}

// annotateTimeline renders run's progressiveHtml as Jenkins writes it while
// the run goes: each stage and step's pipeline-new-node line when it starts,
// each step's log lines spread over its duration, so parallel branches
// interleave, and each ended stage's block end once all inside it ended. A
// running step's lines follow its start. Steps sit in the block their
// stageId names, as PGV's stage ids are the stage body's block start, and
// their [Pipeline] lines name fn(id). The steps in lateLineEnd write their
// log's last line end just after they end, as a durable-task step closing
// its listener after reporting its exit does. stamped renders Timestamper's
// markup, a visible clock and a hidden [ISO] prefix on every line, and adds
// the prefix to run's step logs too, as their progressiveText carries it.
func annotateTimeline(t *testing.T, run *fakeRun, fn func(id string) string, stamped bool, lateLineEnd ...string) string {
	t.Helper()
	type node struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		StageID  string `json:"stageId"`
		Start    int64  `json:"startTimeMillis"`
		Duration int64  `json:"totalDurationMillis"`
		Children []node `json:"children"`
	}
	var tree, steps struct {
		Data struct {
			Stages []node `json:"stages"`
			Steps  []node `json:"steps"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(run.tree), &tree))
	require.NoError(t, json.Unmarshal([]byte(run.allSteps), &steps))

	type event struct {
		at   float64
		text bool
		html string
	}
	var events []event
	stamp := func(at float64) (visible, hidden, text string) {
		if !stamped {
			return "", "", ""
		}
		ts := time.UnixMilli(int64(at)).UTC()
		iso := "[" + ts.Format("2006-01-02T15:04:05.000Z") + "]"
		return `<span class="timestamp"><b>` + ts.Format("15:04:05") + `</b> </span>`,
			`<span style="display: none">` + iso + `</span> `, iso + " "
	}
	mark := func(at float64, attrs, fn string) {
		visible, hidden, _ := stamp(at)
		events = append(events, event{at: at, html: fmt.Sprintf(`%s<span class="pipeline-new-node" %s>%s[Pipeline] %s`+"\n</span>", visible, attrs, hidden, fn)})
	}
	ends := map[string]float64{}
	for _, s := range steps.Data.Steps {
		mark(float64(s.Start), fmt.Sprintf(`nodeId="%s" enclosingId="%s"`, s.ID, s.StageID), fn(s.ID))
		log := run.stepLogs[s.ID]
		if slices.Contains(lateLineEnd, s.ID) {
			log = strings.TrimSuffix(log, "\n")
			events = append(events, event{float64(s.Start+s.Duration) + 0.1, true, fmt.Sprintf(`<span class="pipeline-node-%s">`+"\n</span>", s.ID)})
		}
		lines := slices.Collect(strings.Lines(log))
		end := float64(s.Start + s.Duration)
		var stampedLog strings.Builder
		for k, line := range lines {
			at := float64(s.Start) + float64(k+1)/1000
			if s.State != "running" {
				at = float64(s.Start) + float64(k+1)*float64(s.Duration)/float64(len(lines)+1)
			}
			end = max(end, at)
			visible, hidden, text := stamp(at)
			stampedLog.WriteString(text + line)
			events = append(events, event{at, true, fmt.Sprintf(`<span class="pipeline-node-%s">%s%s%s</span>`, s.ID, visible, hidden, html.EscapeString(line))})
		}
		if stamped {
			run.stepLogs[s.ID] = stampedLog.String() + strings.TrimPrefix(run.stepLogs[s.ID], log)
		}
		ends[s.StageID] = max(ends[s.StageID], end)
	}
	var stages func(ns []node, parent string) float64
	stages = func(ns []node, parent string) float64 {
		var last float64
		for _, n := range ns {
			mark(float64(n.Start), fmt.Sprintf(`nodeId="%s" startId="%s" enclosingId="%s"`, n.ID, n.ID, parent), "stage")
			end := max(float64(n.Start+n.Duration), ends[n.ID], stages(n.Children, n.ID)) + 0.5
			if n.State != "running" {
				mark(end, fmt.Sprintf(`nodeId="e%s" startId="%s"`, n.ID, n.ID), "}")
			}
			last = max(last, end)
		}
		return last
	}
	stages(tree.Data.Stages, "")
	slices.SortStableFunc(events, func(a, b event) int {
		if a.at != b.at {
			return cmp.Compare(a.at, b.at)
		}
		return cmp.Compare(boolInt(a.text), boolInt(b.text))
	})
	var b strings.Builder
	b.WriteString("Started by user admin\n")
	for _, e := range events {
		b.WriteString(e.html)
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// stepDurations matches totalDurationMillis values in a PGV listing.
var stepDurations = regexp.MustCompile(`"totalDurationMillis":\d+`)

// A running run's annotated console stops short, at the line cap or mid-line
// where the log was when the answer ended. The steps it proves whole come
// from it, the rest are read on their own, and the page is the one the
// steps alone give.
func TestCollectRunningConsoleMatchesSteps(t *testing.T) {
	// cut returns where the answer stops in the full console html.
	tests := map[string]struct {
		longLines int
		cut       func(t *testing.T, html string) int
		// maxHits is how many step logs may be read on their own.
		maxHits int32
		// lateLineEnd steps write their last line end after they end.
		lateLineEnd []string
		// stamped renders Timestamper's markup.
		stamped bool
	}{
		"not cut": {
			longLines: 300,
			cut:       func(_ *testing.T, html string) int { return len(html) },
			maxHits:   1,
		},
		"line cap": {
			longLines: 12000,
			cut: func(_ *testing.T, html string) int {
				i := 0
				for range staplertest.MaxLinesRead {
					i += strings.IndexByte(html[i:], '\n') + 1
				}
				return i
			},
			maxHits: 9,
		},
		"mid-node": {
			longLines: 300,
			cut: func(t *testing.T, html string) int {
				i := strings.Index(html, ">line 150 of 14")
				require.Positive(t, i)
				return i + len(">line 1")
			},
			maxHits: 9,
		},
		"timestamper mid-node": {
			longLines: 300,
			stamped:   true,
			cut: func(t *testing.T, html string) int {
				i := strings.Index(html, "</span> line 150 of 14")
				require.Positive(t, i)
				return i + len("</span> line 1")
			},
			maxHits: 9,
		},
		"line end after return": {
			longLines:   300,
			lateLineEnd: []string{"8"},
			cut: func(t *testing.T, html string) int {
				i := strings.Index(html, `nodeId="9" `)
				require.Positive(t, i)
				return i + strings.Index(html[i:], "</span>") + len("</span>")
			},
			maxHits: 11,
		},
		"node boundary": {
			longLines: 300,
			cut: func(t *testing.T, html string) int {
				i := strings.Index(html, `<span class="pipeline-node-41">`)
				require.Positive(t, i)
				return i + strings.Index(html[i:], "</span>") + len("</span>")
			},
			maxHits: 6,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			run := loadRun(t, "mid", "scripted")
			for i, id := range scriptedIDs {
				run.stepLogs[id] = strings.Repeat(fmt.Sprintf("out of step %s\n", id), 10*(i+1))
			}
			var long strings.Builder
			for i := range tc.longLines {
				fmt.Fprintf(&long, "line %d of 14\n", i)
			}
			run.stepLogs["14"] = long.String()
			run.stepLogs["53"] = "slow 1\nslow 2\n"
			if len(tc.lateLineEnd) > 0 {
				run.stepLogs["8"] = "foo\n"
			}
			run.open["53"] = true
			run.exceptions["21"] = "script returned exit code 3"
			run.console = "Started by user admin\n"
			fn := func(id string) string {
				if slices.Contains([]string{"8", "24", "36", "46"}, id) {
					return "echo"
				}
				return "sh"
			}
			full := annotateTimeline(t, run, fn, tc.stamped, tc.lateLineEnd...)
			run.consoleHTML = full[:tc.cut(t, full)]
			run.consoleHTMLMore = true
			// Step durations are no proof: PGV's can end long before the step.
			run.allSteps = stepDurations.ReplaceAllString(run.allSteps, `"totalDurationMillis":1`)
			client := run.serve(t)

			want := renderPage(t, client, run.job, Options{LogSource: LogSourceSteps, Concurrency: 4})
			run.stepLogHits.Store(0)

			var notices []string
			got := renderPage(t, client, run.job, Options{Concurrency: 4,
				Notice: func(msg string) { notices = append(notices, msg) }})
			assert.Equal(t, want, got)
			assert.Equal(t, int32(1), run.htmlHits.Load())
			hits := run.stepLogHits.Load()
			assert.Positive(t, hits, "the running step is read on its own")
			assert.LessOrEqual(t, hits, tc.maxHits)
			require.Len(t, notices, 2)
			assert.Contains(t, notices[1], fmt.Sprintf("build running: %d step logs whole in the annotated console, reading the other %d one by one", 11-hits, hits))
		})
	}
}

// parallelCutRun serves the real progressiveHtml of `node { parallel a: {
// sh 'echo a1; sleep 8; echo a2' }, b: { sleep 1; echo 'b' }, c: { sleep 4;
// echo 'c' } }`, edited by edit and cut right after the first span holding
// cutAfter, as a running run. PGV ends step 11, the sh, when branch b ended,
// before branch c's echo started.
func parallelCutRun(t *testing.T, edit func(string) string, cutAfter string) *fakeRun {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "consolehtml", "parallel-cut."+name))
		require.NoError(t, err)
		return string(b)
	}
	run := loadRun(t, "mid", "scripted")
	run.tree, run.allSteps = read("tree.json"), read("allSteps.json")
	require.Contains(t, run.allSteps, `"id":"11"`)
	run.stepLogs = map[string]string{"11": "+ echo a1\na1\n+ sleep 8\n+ echo a2\na2\n", "12": "Sleeping for 1 sec\n",
		"13": "Sleeping for 4 sec\n", "14": "b\n", "16": "c\n"}
	full := edit(read("html"))
	i := strings.Index(full, cutAfter)
	require.Positive(t, i)
	i += strings.Index(full[i:], "</span>") + len("</span>")
	run.consoleHTML = full[:i]
	run.consoleHTMLMore = true
	run.console = "Started by user admin\n"
	return run
}

// Cut after branch c's echo, the sh wrote a2 after the cut.
func TestCollectRunningParallelCut(t *testing.T) {
	run := parallelCutRun(t, func(h string) string { return h }, `<span class="pipeline-node-16">c`)
	client := run.serve(t)
	want := renderPage(t, client, run.job, Options{LogSource: LogSourceSteps})
	run.stepLogHits.Store(0)

	snap, err := collect(t, client, run.job, Options{})
	require.NoError(t, err)
	var sh *Step
	snap.EachStep(func(_ *Node, s *Step) {
		if s.ID == "11" {
			sh = s
		}
	})
	require.NotNil(t, sh)
	assert.Equal(t, int64(1040), sh.DurationMillis, "PGV's end for the sh is branch b's")
	assert.Equal(t, run.stepLogs["11"], decode(t, sh.Log))
	snap.ExportedAtMillis = 0
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, snap))
	assert.Equal(t, want, buf.String())
	// Step 14, an echo, is proved by branch b's end. The sleeps are not
	// among flushedSteps, and step 16's branch end follows the cut.
	assert.Equal(t, int32(4), run.stepLogHits.Load(), "steps 11, 12, 13 and 16 read on their own")
}

// The sh's output ends in an AnsiColor escape, which the annotated console
// carries in a comment, so the output did not end a line and closing the
// listener writes one after branch a's end, past the cut.
func TestCollectRunningHiddenOutputEnd(t *testing.T) {
	run := parallelCutRun(t, func(h string) string {
		out := strings.Replace(h, "a2\n</span>", "a2\n<!--\x1b[0m--></span>", 1)
		require.NotEqual(t, h, out)
		return out
	}, `nodeId="18"`)
	run.stepLogs["11"] += "\x1b[0m\n"
	client := run.serve(t)
	want := renderPage(t, client, run.job, Options{LogSource: LogSourceSteps})
	run.stepLogHits.Store(0)

	snap, err := collect(t, client, run.job, Options{})
	require.NoError(t, err)
	snap.EachStep(func(_ *Node, s *Step) {
		if s.ID == "11" {
			assert.Equal(t, "+ echo a1\na1\n+ sleep 8\n+ echo a2\na2\n\n", decode(t, s.Log))
		}
	})
	snap.ExportedAtMillis = 0
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, snap))
	assert.Equal(t, want, buf.String())
	assert.Equal(t, int32(3), run.stepLogHits.Load(), "steps 11, 12 and 13 read on their own")
}

func TestWholeIn(t *testing.T) {
	// Block 4 holds atom 5, block 6 (holding atom 7) and atom 9; 8 ends
	// block 6 and 10 ends block 4. 3 is a block step whose end is 11.
	marks := []nodeMark{
		{id: "3", start: "3", enclosing: "2", fn: "sh", pos: 0},
		{id: "4", start: "4", enclosing: "3", pos: 0},
		{id: "5", enclosing: "4", fn: "sh", pos: 0},
		{id: "6", start: "6", enclosing: "4", pos: 10},
		{id: "7", enclosing: "6", fn: "echo", pos: 10},
		{id: "8", start: "6", pos: 20},
		{id: "9", enclosing: "4", fn: "bat", pos: 20},
		{id: "10", start: "4", pos: 30},
		{id: "11", start: "3", pos: 30},
		{id: "12", enclosing: "2", fn: "sh", pos: 30},
		{id: "13", enclosing: "2", fn: "checkout", pos: 30},
		{id: "14", enclosing: "2", fn: "sh", pos: 30},
	}
	l := &markLog{self: map[string]int{}, inBlock: map[string][]int{}, ends: map[string][]int{}}
	for _, m := range marks {
		l.add(m)
	}
	raw := strings.NewReader("aaaaaaaaa\nbbbbbbbbb\nccccccccc\n")
	done := func(id string) *Step { return &Step{ID: id, State: "SUCCESS"} }
	assert.True(t, wholeIn(done("5"), []textRun{{0, 10}}, l, raw), "proved by the block 6 that follows")
	assert.True(t, wholeIn(done("7"), []textRun{{10, 10}}, l, raw), "proved by block 6's end")
	assert.True(t, wholeIn(done("9"), nil, l, raw), "no output, proved by block 4's end")
	assert.True(t, wholeIn(done("3"), []textRun{{0, 10}}, l, raw), "block step, proved by its end")
	assert.False(t, wholeIn(done("5"), []textRun{{0, 5}}, l, raw), "no line end: closing the listener may add one")
	assert.False(t, wholeIn(done("5"), []textRun{{0, 5}, {15, 5}}, l, raw), "wrote after the proof")
	assert.False(t, wholeIn(done("13"), nil, l, raw), "not among flushedSteps")
	assert.False(t, wholeIn(done("14"), nil, l, raw), "nothing after it")
	assert.False(t, wholeIn(done("99"), nil, l, raw), "its mark never arrived")
	assert.False(t, wholeIn(&Step{ID: "5", State: "IN_PROGRESS", Running: true}, nil, l, raw))
	assert.False(t, wholeIn(&Step{ID: "5"}, nil, l, raw), "unknown state")
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
			run.consoleHits.Store(0)

			var notices []string
			dir := t.TempDir()
			exportOpts := Options{LogSource: LogSourceConsole, Concurrency: 4, SpoolDir: dir,
				Notice: func(msg string) { notices = append(notices, msg) }}
			snap, err := Collect(context.Background(), client, run.job, 1, exportOpts)
			require.NoError(t, err)
			snap.ExportedAtMillis = 0
			var buf bytes.Buffer
			require.NoError(t, Render(&buf, snap))
			require.NoError(t, snap.Close())
			assert.Equal(t, want, buf.String())
			assert.Zero(t, openSpools(t, dir), "spools closed")
			assert.Equal(t, int32(1), run.consoleHits.Load(), "plain console read once")
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
