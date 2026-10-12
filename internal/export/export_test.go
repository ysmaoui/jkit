package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
	"github.com/ysmaoui/jkit/internal/staplertest"
)

const note = "\x1b[8mha:////4LKcdmFzZQ==\x1b[0m"

// fakeRun is a pipeline run served the way Jenkins with PGV serves it.
type fakeRun struct {
	job        string
	mode       staplertest.Mode
	building   bool
	tree       string // stages/tree body
	allSteps   string // stages/allSteps body
	stepLogs   map[string]string
	open       map[string]bool
	exceptions map[string]string
	failLog    map[string]int // step id -> status its log route answers
	console    string
	// stall sends the first line of a log, then holds the body open.
	stall map[string]bool // step id or "console"
	// holdHeaders delays a step log's response until the client gives up.
	holdHeaders map[string]bool
	// failException is the status a step's exceptionText answers.
	failException map[string]int
	// delay holds a step log's response back this long.
	delay map[string]time.Duration
	// consoleHTML is the progressiveHtml body, which 404s when empty.
	// consoleHTMLMore sets X-More-Data, as for a running build.
	consoleHTML     string
	consoleHTMLMore bool
	// htmlHits and stepLogHits count requests to those routes.
	htmlHits, stepLogHits atomic.Int32
}

func loadRun(t *testing.T, phase, kind string) *fakeRun {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(fmt.Sprintf("testdata/%s-%s-%s.json", phase, kind, name))
		require.NoError(t, err)
		return string(b)
	}
	return &fakeRun{
		job:        "exp-" + kind,
		mode:       staplertest.Streaming,
		building:   phase == "mid",
		tree:       read("tree"),
		allSteps:   read("allSteps"),
		stepLogs:   map[string]string{},
		open:       map[string]bool{},
		exceptions: map[string]string{},
		failLog:    map[string]int{},
		stall:      map[string]bool{},

		holdHeaders:   map[string]bool{},
		failException: map[string]int{},
		delay:         map[string]time.Duration{},
	}
}

// stallBody answers as a plain progressiveText or consoleText would.
func stallBody(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Text-Size", "11")
	_, _ = io.WriteString(w, "first line\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func (f *fakeRun) serve(t *testing.T, opts ...api.ClientOption) *api.Client {
	t.Helper()
	prefix := "/job/" + f.job + "/1/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api/json":
			staplertest.WriteRoot(w, f.mode)
		case p == prefix+"api/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun", "number": 1,
				"result": map[bool]any{true: nil, false: "FAILURE"}[f.building], "building": f.building,
				"timestamp": 1791791175000, "duration": map[bool]int{true: 0, false: 76000}[f.building],
				"url": "http://jenkins/job/" + f.job + "/1/",
			})
		case p == prefix+"stages/tree":
			_, _ = io.WriteString(w, f.tree)
		case p == prefix+"stages/allSteps":
			_, _ = io.WriteString(w, f.allSteps)
		case p == prefix+"stages/exceptionText" && f.failException[r.URL.Query().Get("nodeId")] != 0:
			w.WriteHeader(f.failException[r.URL.Query().Get("nodeId")])
		case p == prefix+"stages/exceptionText":
			_, _ = io.WriteString(w, f.exceptions[r.URL.Query().Get("nodeId")])
		case p == prefix+"consoleText" && f.stall["console"]:
			stallBody(w, r)
		case p == prefix+"consoleText" && f.failLog["console"] != 0:
			w.WriteHeader(f.failLog["console"])
		case p == prefix+"consoleText":
			staplertest.ConsoleText(w, f.console)
		case p == prefix+"logText/progressiveHtml":
			f.htmlHits.Add(1)
			switch {
			case f.stall["html"]:
				stallBody(w, r)
			case f.failLog["html"] != 0:
				w.WriteHeader(f.failLog["html"])
			case f.consoleHTML == "":
				w.WriteHeader(http.StatusNotFound)
			default:
				w.Header().Set("X-Text-Size", strconv.Itoa(len(f.consoleHTML)))
				if f.consoleHTMLMore {
					w.Header().Set("X-More-Data", "true")
				}
				_, _ = io.WriteString(w, f.consoleHTML)
			}
		case strings.HasPrefix(p, prefix+"execution/node/"):
			id, rest, _ := strings.Cut(strings.TrimPrefix(p, prefix+"execution/node/"), "/")
			if rest == "" {
				_, _ = io.WriteString(w, "<html>flow node</html>")
				return
			}
			f.stepLogHits.Add(1)
			if d := f.delay[id]; d > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(d):
				}
			}
			if f.holdHeaders[id] {
				<-r.Context().Done()
				return
			}
			if f.stall[id] {
				stallBody(w, r)
				return
			}
			if code := f.failLog[id]; code != 0 {
				w.WriteHeader(code)
				return
			}
			log, ok := f.stepLogs[id]
			if !ok || rest != "log/logText/progressiveText" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			staplertest.WriteProgressive(w, r, f.mode, log, f.open[id])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return api.NewClient(srv.URL, "u", "t", opts...)
}

func testSpool(t *testing.T) *spool {
	t.Helper()
	sp, err := newSpool(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sp.close() })
	return sp
}

func collect(t *testing.T, client *api.Client, job string, opts Options) (*Snapshot, error) {
	t.Helper()
	snap, err := Collect(context.Background(), client, job, 1, opts)
	if snap != nil {
		t.Cleanup(func() { _ = snap.Close() })
	}
	return snap, err
}

func content(t *testing.T, l Log) string {
	t.Helper()
	b, err := io.ReadAll(l.content())
	require.NoError(t, err)
	return string(b)
}

func decode(t *testing.T, l Log) string {
	t.Helper()
	text := content(t, l)
	if text == "" {
		return ""
	}
	zr, err := gzip.NewReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(text)))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(raw)
}

func find(nodes []Node, id string) *Node {
	var found *Node
	walk(nodes, func(n *Node) {
		if n.ID == id {
			found = n
		}
	})
	return found
}

func stepIDs(n *Node) []string {
	ids := []string{}
	for _, s := range n.Steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestCollectFinishedScripted(t *testing.T) {
	run := loadRun(t, "done", "scripted")
	for id := range map[string]bool{"8": true, "9": true, "14": true, "21": true, "24": true, "35": true, "42": true, "46": true, "36": true, "41": true, "53": true} {
		run.stepLogs[id] = "out of step " + id + "\n"
	}
	run.stepLogs["21"] = "+ echo about to fail\nabout to fail\n+ exit 3\n"
	run.exceptions["21"] = "script returned exit code 3"
	run.console = "Started by user admin\n" + note + "[Pipeline] node\nRunning on Jenkins\nERROR: script returned exit code 3\n"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)

	assert.True(t, snap.Complete)
	assert.Equal(t, "FAILURE", snap.Result)
	assert.Equal(t, int64(1791791175000), snap.StartTimeMillis)
	assert.Positive(t, snap.ExportedAtMillis)

	var top []string
	for _, n := range snap.Stages {
		top = append(top, n.Name)
	}
	assert.Equal(t, []string{"Build", "LongLog", "Fail", "Par", "Slow"}, top)

	par := find(snap.Stages, "28")
	require.NotNil(t, par)
	assert.Equal(t, "STAGE", par.Type)
	assert.Empty(t, par.Steps, "a parallel wrapper has no steps of its own")
	require.Len(t, par.Children, 2)
	assert.Equal(t, "PARALLEL", par.Children[0].Type)
	assert.Equal(t, []string{"A1", "A2"}, []string{par.Children[0].Children[0].Name, par.Children[0].Children[1].Name})
	assert.Equal(t, []string{"36"}, stepIDs(find(snap.Stages, "34")))
	assert.Equal(t, []string{"35", "42", "46"}, stepIDs(find(snap.Stages, "32")), "branch without nested stage holds its steps")
	assert.Equal(t, []string{"8", "9"}, stepIDs(find(snap.Stages, "7")))

	sleep := find(snap.Stages, "32").Steps[1]
	assert.Equal(t, "Sleep", sleep.Title)
	assert.Equal(t, "5", sleep.Name)
	assert.Equal(t, int64(5011), sleep.DurationMillis)

	failed := find(snap.Stages, "18").Steps[0]
	assert.Equal(t, "FAILURE", failed.State)
	assert.Equal(t, "script returned exit code 3", failed.Exception)
	assert.Equal(t, "+ echo about to fail\nabout to fail\n+ exit 3\n", decode(t, failed.Log))
	assert.Equal(t, int64(len("+ echo about to fail\nabout to fail\n+ exit 3\n")), failed.Log.Bytes)
	assert.Empty(t, find(snap.Stages, "18").Steps[1].Exception)

	console := decode(t, snap.Console)
	assert.Contains(t, console, "Running on Jenkins\nERROR: script returned exit code 3\n")
	assert.NotContains(t, console, "ha:")
}

func TestCollectDeclarative(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.stepLogs["7"] = "building\n"
	run.exceptions["14"] = "boom from error step"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)

	fail := find(snap.Stages, "11")
	require.Len(t, fail.Steps, 1)
	errStep := fail.Steps[0]
	assert.Equal(t, "Error signal", errStep.Title)
	assert.Equal(t, "boom from error step", errStep.Exception)
	assert.Equal(t, Log{}, errStep.Log, "error step writes no log")
	assert.Empty(t, errStep.Error)

	post := snap.Stages[len(snap.Stages)-1]
	assert.Equal(t, "Post Actions", post.Name)
	assert.True(t, post.Synthetic)
	assert.Equal(t, []string{"56"}, stepIDs(&post))

	assert.Equal(t, []string{"31"}, stepIDs(find(snap.Stages, "23")))
	assert.Equal(t, []string{"40"}, stepIDs(find(snap.Stages, "39")))
}

func TestCollectRunning(t *testing.T) {
	run := loadRun(t, "mid", "scripted")
	run.mode = staplertest.Uncounted
	var long strings.Builder
	for i := range 12000 {
		fmt.Fprintf(&long, "slow %d\n", i)
	}
	run.stepLogs["53"] = long.String() + "tick 1\nunterminated"
	run.open["53"] = true
	run.console = "Started\n[Pipeline] stage\nslow 1\nunterm"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)

	assert.False(t, snap.Complete)
	assert.True(t, snap.Building)
	slow := find(snap.Stages, "52")
	assert.True(t, slow.Running)
	assert.Equal(t, "IN_PROGRESS", slow.State)
	assert.Zero(t, slow.DurationMillis)
	step := slow.Steps[0]
	assert.True(t, step.Running)
	assert.True(t, step.Log.Partial, "a plain answer on 2.509-2.533 stops at the line cap")
	text := decode(t, step.Log)
	assert.True(t, strings.HasSuffix(text, "\n"))
	assert.Equal(t, staplertest.MaxLinesRead, strings.Count(text, "\n"))
	assert.False(t, find(snap.Stages, "7").Running)
	assert.Equal(t, "Started\n[Pipeline] stage\nslow 1\nunterm", decode(t, snap.Console))
}

func TestCollectStripsNotesAndEscapes(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	// A plain answer keeps notes and sends CRLF.
	run.mode = staplertest.Counted
	run.stepLogs["7"] = note + "[Pipeline] echo\n\x1b[31mred\x1b[0m\tcol\n"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	assert.Equal(t, "[Pipeline] echo\nred\tcol\n", decode(t, find(snap.Stages, "6").Steps[0].Log))
}

func TestCollectRecordsStepLogFailure(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.failLog["7"] = http.StatusInternalServerError
	run.stepLogs["31"] = "pa\n"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	assert.Contains(t, find(snap.Stages, "6").Steps[0].Error, "500")
	assert.True(t, find(snap.Stages, "6").Steps[0].Log.Partial, "a failed read marks the log partial")
	assert.Equal(t, "pa\n", decode(t, find(snap.Stages, "23").Steps[0].Log))
}

func TestCollectStopsOnAuthFailure(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.failLog["7"] = http.StatusUnauthorized

	_, err := collect(t, run.serve(t), run.job, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth login")
}

func TestCollectStopsOnConsoleAuthFailure(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.failLog["console"] = http.StatusUnauthorized

	_, err := collect(t, run.serve(t), run.job, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth login")
}

func TestCollectTimeoutSaysRaiseTimeout(t *testing.T) {
	t.Run("console is recorded", func(t *testing.T) {
		run := loadRun(t, "done", "declarative")
		run.stall["console"] = true
		run.stepLogs["51"] = "kept\n"
		snap, err := collect(t, run.serve(t, api.WithTimeout(300*time.Millisecond)), run.job, Options{})
		require.NoError(t, err)
		assert.Contains(t, snap.ConsoleError, "raise it with --timeout")
		assert.True(t, snap.Console.Partial)
		assert.Equal(t, "first line\n", decode(t, snap.Console), "what arrived is kept")
		assert.Equal(t, "kept\n", decode(t, find(snap.Stages, "50").Steps[0].Log), "step logs survive")
	})
	t.Run("step log is recorded", func(t *testing.T) {
		run := loadRun(t, "done", "declarative")
		run.stall["51"] = true
		snap, err := collect(t, run.serve(t, api.WithTimeout(300*time.Millisecond)), run.job, Options{})
		require.NoError(t, err)
		step := find(snap.Stages, "50").Steps[0]
		assert.Contains(t, step.Error, "raise it with --timeout")
		assert.Equal(t, "first line\n", decode(t, step.Log), "what arrived is kept")
	})
}

func TestCollectRecordsHeaderTimeoutOnStep(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.holdHeaders["51"] = true
	run.stepLogs["56"] = "post always\n"

	snap, err := collect(t, run.serve(t, api.WithTimeout(100*time.Millisecond)), run.job, Options{})
	require.NoError(t, err)
	assert.Contains(t, find(snap.Stages, "50").Steps[0].Error, "raise it with --timeout")
	assert.Equal(t, "post always\n", decode(t, find(snap.Stages, "55").Steps[0].Log))
}

func TestCollectRecordsExceptionFailure(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.failException["14"] = http.StatusInternalServerError
	run.stepLogs["56"] = "post always\n"

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	step := find(snap.Stages, "11").Steps[0]
	assert.Contains(t, step.Error, "500")
	assert.Empty(t, step.Exception)
	assert.Equal(t, "post always\n", decode(t, find(snap.Stages, "55").Steps[0].Log))
}

func TestCollectStopsWhenPluginMissing(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.failException["14"] = http.StatusNotFound

	_, err := collect(t, run.serve(t), run.job, Options{})
	var pme *jenkins.PluginMissingError
	require.ErrorAs(t, err, &pme)
	assert.Equal(t, "stages/exceptionText", pme.Route)
}

func TestCollectCapsLogs(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	var long strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&long, "line %04d\n", i)
	}
	run.stepLogs["51"] = long.String()
	run.console = long.String()

	snap, err := collect(t, run.serve(t), run.job, Options{MaxLogBytes: 100})
	require.NoError(t, err)
	for _, l := range []Log{find(snap.Stages, "50").Steps[0].Log, snap.Console} {
		text := decode(t, l)
		assert.Equal(t, int64(10000), l.Bytes)
		assert.True(t, strings.HasPrefix(text, "line 0000\nline 0001\n"), text)
		assert.True(t, strings.HasSuffix(text, "line 0999\n"), text)
		assert.Contains(t, text, fmt.Sprintf("\n[jkit: %d bytes cut]\n", l.CutBytes))
		assert.Less(t, len(text), 150)
	}
}

func TestCollectUnlistedStage(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.allSteps = strings.Replace(run.allSteps, `"stageId":"55"`, `"stageId":"99"`, 1)

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	last := snap.Stages[len(snap.Stages)-1]
	assert.Equal(t, UnlistedType, last.Type)
	assert.Equal(t, "99", last.ID)
	assert.Equal(t, []string{"56"}, stepIDs(&last))
	assert.Empty(t, find(snap.Stages, "55").Steps)
}

func TestCollectDropsPipelineStart(t *testing.T) {
	run := loadRun(t, "mid", "declarative")
	run.tree = `{"status":"ok","data":{"complete":false,"stages":[{"id":"2","name":"System Generated","type":"PIPELINE_START","state":"running","children":[]}]}}`
	run.allSteps = `{"status":"ok","data":{"runIsComplete":false,"steps":[]}}`

	snap, err := collect(t, run.serve(t), run.job, Options{})
	require.NoError(t, err)
	assert.Empty(t, snap.Stages)
	assert.NotNil(t, snap.Stages, "encodes as [] for the template")
}

func TestEncodeLogCap(t *testing.T) {
	tests := []struct {
		name, in, want string
		max, cut       int64
	}{
		{"under cap", "a\nb\n", "a\nb\n", 10, 0},
		{"exactly cap", "abcd\nefgh\n", "abcd\nefgh\n", 10, 0},
		{"no cap", "abcdefghij\n", "abcdefghij\n", 0, 0},
		{"cut at line ends", "aaa\nbbb\nccc\nddd\n", "aaa\n[jkit: 8 bytes cut]\nddd\n", 8, 8},
		{"cut drops partial tail line", "aaaa\nbb\ncc\n", "aaaa\n[jkit: 4 bytes cut]\ncc\n", 8, 4},
		{"tail with one line end kept whole", "aaaaaa\nbbbbbb\n", "aaa\n[jkit: 8 bytes cut]\nbb\n", 6, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Writing one byte at a time exercises the ring wrapping.
			l, err := encodeLog(testSpool(t), tc.max, func(w io.Writer) error {
				for i := range len(tc.in) {
					if _, err := w.Write([]byte{tc.in[i]}); err != nil {
						return err
					}
				}
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, decode(t, l))
			assert.Equal(t, int64(len(tc.in)), l.Bytes)
			assert.Equal(t, tc.cut, l.CutBytes)
		})
	}
}

func TestEncodeLogCapKeepsRunesWhole(t *testing.T) {
	in := strings.Repeat("日", 20) // 3 bytes each, no line ends
	for _, max := range []int64{7, 8, 9, 10, 11} {
		t.Run(fmt.Sprint(max), func(t *testing.T) {
			l, err := encodeLog(testSpool(t), max, func(w io.Writer) error {
				_, err := io.WriteString(w, in)
				return err
			})
			require.NoError(t, err)
			text := decode(t, l)
			assert.True(t, utf8.ValidString(text), "%q", text)
			head, tail, ok := strings.Cut(text, fmt.Sprintf("\n[jkit: %d bytes cut]\n", l.CutBytes))
			require.True(t, ok, "%q", text)
			assert.Equal(t, int64(len(in)), int64(len(head)+len(tail))+l.CutBytes)
		})
	}
}

func TestSanitizerSplitsOverlongLineAtRune(t *testing.T) {
	var out bytes.Buffer
	s := &sanitizer{w: &out}
	in := strings.Repeat("x", maxLineBytes-1) + "日\x1b[1m日\n"
	for i := range len(in) {
		_, err := s.Write([]byte{in[i]})
		require.NoError(t, err)
	}
	require.NoError(t, s.flush())
	assert.Equal(t, strings.Repeat("x", maxLineBytes-1)+"日日\n", out.String())
}

func TestSanitizerCRLF(t *testing.T) {
	for _, in := range []string{"a\r\n", "a\r\r\n", "\r\n", "a\x1b[31mb\r\n", "a\rb\r\n", "x\r"} {
		var out bytes.Buffer
		s := &sanitizer{w: &out}
		_, err := io.WriteString(s, in)
		require.NoError(t, err)
		require.NoError(t, s.flush())
		want := ""
		for _, line := range strings.SplitAfter(in, "\n") {
			text, eol := strings.CutSuffix(line, "\n")
			want += output.StripControl(output.SanitizeLog(text))
			if eol {
				want += "\n"
			}
		}
		assert.Equal(t, want, out.String(), "%q", in)
	}
}

func TestEncodeLogKeepsWhatArrivedOnError(t *testing.T) {
	l, err := encodeLog(testSpool(t), 0, func(w io.Writer) error {
		_, _ = io.WriteString(w, "first\nsec")
		return io.ErrUnexpectedEOF
	})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, "first\nsec", decode(t, l))
}

func TestSanitizerSplitsOverlongLine(t *testing.T) {
	var out bytes.Buffer
	s := &sanitizer{w: &out}
	in := strings.Repeat("x", maxLineBytes+10) + "\n"
	_, err := s.Write([]byte(in))
	require.NoError(t, err)
	require.NoError(t, s.flush())
	assert.Equal(t, in, out.String())
}

func TestEncodeLogSpoolFailureStopsExport(t *testing.T) {
	sp := testSpool(t)
	require.NoError(t, sp.f.Close())
	_, err := encodeLog(sp, 0, func(w io.Writer) error {
		_, err := io.WriteString(w, strings.Repeat("x\n", 1<<16))
		return err
	})
	require.ErrorIs(t, err, errSpool)
	assert.True(t, stopsExport(context.Background(), err))
}

func TestCollectLeavesNoSpoolFile(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	dir := t.TempDir()
	snap, err := Collect(context.Background(), run.serve(t), run.job, 1, Options{SpoolDir: dir})
	require.NoError(t, err)
	require.NoError(t, snap.Close())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
