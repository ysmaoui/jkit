package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

func TestLFWriterUndoesCRLFRewrite(t *testing.T) {
	tests := map[string]struct {
		writes []string
		want   string
	}{
		"rewritten LF":        {[]string{"a\r\nb\r\n"}, "a\nb\n"},
		"CR split from LF":    {[]string{"a\r", "\nb"}, "a\nb"},
		"lone CR kept":        {[]string{"50%\r60%\r\n"}, "50%\r60%\n"},
		"lone CR at boundary": {[]string{"50%\r", "60%"}, "50%\r60%"},
		"trailing CR flushed": {[]string{"a\r"}, "a\r"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			w := &lfWriter{w: &out}
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				require.NoError(t, err)
				assert.Equal(t, len(s), n)
			}
			require.NoError(t, w.Flush())
			assert.Equal(t, tt.want, out.String())
		})
	}
}

func TestCountsTextSize(t *testing.T) {
	for version, want := range map[string]bool{
		"2.479.3": true, "2.504.3": true, "2.508": true,
		"2.509": false, "2.516.3": false, "2.568.3": false,
		"": false, "garbage": false,
	} {
		assert.Equal(t, want, countsTextSize(version), version)
	}
}

// consoleServer serves build 1 of job "app" as Jenkins in mode would, from a
// log the test changes between reads. It records every progressiveText start
// and how often the root api/json was asked for the version. behind sums how
// far each start lay behind the end of the log then, which bounds what the
// answers could send.
type consoleServer struct {
	*httptest.Server
	mu       sync.Mutex
	log      string
	open     bool
	starts   []int64
	behind   int64
	versions int
}

func newConsoleServer(t *testing.T, mode staplertest.Mode) *consoleServer {
	t.Helper()
	s := &consoleServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/json":
			s.versions++
			staplertest.WriteRoot(w, mode)
		case "/job/app/1/logText/progressiveText":
			start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			s.starts = append(s.starts, start)
			if start >= 0 {
				s.behind += max(int64(len(s.log))-start, 0)
			}
			staplertest.WriteProgressive(w, r, mode, s.log, s.open)
		case "/job/app/1/consoleText":
			staplertest.ConsoleText(w, s.log)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *consoleServer) set(log string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log, s.open = log, open
}

func (s *consoleServer) takeStarts() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	starts := s.starts
	s.starts = nil
	return starts
}

const testNote = "\x1b[8mha:////NOTE\x1b[0m"

var testNoteRe = regexp.MustCompile(`\x1b\[8mha:.*?\x1b\[0m`)

// shown is what a mode's answers show of a stored log: a streaming answer
// strips notes and keeps line ends, a plain one keeps notes and loses the CR
// of a CRLF.
func shown(mode staplertest.Mode, log string) string {
	if mode == staplertest.Streaming {
		return testNoteRe.ReplaceAllString(log, "")
	}
	return strings.ReplaceAll(log, "\r\n", "\n")
}

func lines(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%05d\n", prefix, i)
	}
	return b.String()
}

// TestConsoleLogFollow follows a console that gains a note, an unterminated
// line, a CRLF the build wrote, and a burst of more lines than a plain answer
// holds, then completes. Every mode must write each byte exactly once, and
// each poll must ask from where its mode says the last one stopped.
func TestConsoleLogFollow(t *testing.T) {
	s0 := testNote + "Started\nl1\n"
	s1 := s0 + "l2\npart"
	s2 := s1 + "ial\nl3\r\nl4\n"
	burst := lines("b", staplertest.MaxLinesRead+5)
	s3 := s2 + burst
	s4 := s3 + "Finished\n"
	n0, n1, n2, n3 := int64(len(s0)), int64(len(s1)), int64(len(s2)), int64(len(s3))
	// Offset in s3 where the first MaxLinesRead lines of the burst end.
	capped := n2 + int64(len(lines("b", staplertest.MaxLinesRead)))

	polls := []struct {
		log  string
		open bool
	}{{s0, true}, {s1, true}, {s2, true}, {s3, true}, {s3, true}, {s4, false}}

	cases := map[staplertest.Mode][][]int64{
		// No line cap. Each poll searches for the last LF before the reported
		// end with searchNewLineUntil. The byte before the reported end goes
		// first while the last search found the LF there, as at poll 1. At
		// poll 2 it is not, and where the text written puts the LF is right,
		// as the next offset has none; poll 3 starts there. Once the anchor
		// is at the reported end, nothing is left to search.
		staplertest.Streaming: {{0}, {n0 - 1}, {n1 - 1, n0 + 2, n0 + 3}, {n2 - 1}, {n3 - 1}, {n3}},
		// Every answer says where it stopped.
		staplertest.Counted: {{0}, {n0}, {n0 + 3}, {n2}, {capped}, {n3}},
		// The burst passes the line cap from the anchor at n2, so the rest
		// waits for the log to complete, checked without reading text.
		staplertest.Uncounted: {{0}, {n0 - 1}, {n1 - 1, n0}, {n2 - 1}, {n2}, {n2, n2}},
	}
	for mode, wantStarts := range cases {
		t.Run(mode.String(), func(t *testing.T) {
			srv := newConsoleServer(t, mode)
			l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
			var out strings.Builder
			for i, p := range polls {
				srv.set(p.log, p.open)
				more, err := l.Read(context.Background(), &out)
				require.NoError(t, err, "poll %d", i)
				assert.Equal(t, p.open, more, "poll %d", i)
				assert.Equal(t, wantStarts[i], srv.takeStarts(), "poll %d", i)
			}
			assert.Equal(t, shown(mode, s4), out.String())
		})
	}
}

func TestConsoleLogFollowStalledWritesUpToCap(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Uncounted)
	l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
	log := lines("x", staplertest.MaxLinesRead+3)
	srv.set(log, true)

	var out strings.Builder
	more, err := l.Read(context.Background(), &out)
	require.NoError(t, err)
	assert.True(t, more)
	assert.True(t, l.Stalled())
	assert.Equal(t, lines("x", staplertest.MaxLinesRead), out.String())

	srv.set(log, false)
	more, err = l.Read(context.Background(), &out)
	require.NoError(t, err)
	assert.False(t, more)
	assert.False(t, l.Stalled())
	assert.Equal(t, log, out.String())
}

// TestConsoleLogVersionFromRoot checks that the version comes from api/json,
// asked once: Jenkins does not send X-Jenkins with progressiveText.
func TestConsoleLogVersionFromRoot(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Counted)
	l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
	var out strings.Builder
	for _, log := range []string{"a\n", "a\nb\n", "a\nb\nc\n"} {
		srv.set(log, true)
		_, err := l.Read(context.Background(), &out)
		require.NoError(t, err)
	}
	assert.Equal(t, "a\nb\nc\n", out.String())
	assert.Equal(t, []int64{0, 2, 4}, srv.takeStarts())
	assert.Equal(t, 1, srv.versions)
}

func TestConsoleLogRequiresTextSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "not a progressiveText answer")
	}))
	defer srv.Close()

	var out strings.Builder
	_, err := NewClient(srv.URL, "u", "t").ConsoleLog("svc", 5).Read(context.Background(), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "X-Text-Size")
	assert.Empty(t, out.String())
}

func TestConsoleLogStreamingWithoutMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
		_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\ncut off")
	}))
	defer srv.Close()

	var out strings.Builder
	_, err := NewClient(srv.URL, "u", "t").ConsoleLog("svc", 5).Read(context.Background(), &out)
	require.Error(t, err)
}

// TestProgressiveLogRestarted covers Stapler answering from offset 0 when the
// stored log is shorter than start. A plain answer is caught before its body
// is written; a streaming one only after its text, which is already out.
func TestProgressiveLogRestarted(t *testing.T) {
	cases := map[string]struct {
		answer  func(w http.ResponseWriter)
		written string
	}{
		"streaming": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\nfrom the top\n"+
				"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"start\":0,\"end\":13}\r\n--b--")
		}, "from the top\n"},
		"plain": {func(w http.ResponseWriter) {
			w.Header().Set("X-Text-Size", "13")
			w.Header().Set("X-Jenkins", "2.479.3")
			w.Header().Set("X-More-Data", "true")
			_, _ = fmt.Fprint(w, "from the top\r\n")
		}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.answer(w) }))
			defer srv.Close()

			var out strings.Builder
			_, err := NewClient(srv.URL, "u", "t").NewProgressiveLog("/log", 40).Read(context.Background(), &out)
			assert.ErrorIs(t, err, errLogRestarted)
			assert.Equal(t, tc.written, out.String())
		})
	}
}

// TestGetBuildLogTailRunning reads the tail of a running log in every mode,
// through a window that fits in one answer and one that passes the line cap.
// The tail ends at the last complete line, and holds the true last lines.
func TestGetBuildLogTailRunning(t *testing.T) {
	body := lines("t", staplertest.MaxLinesRead+2000)
	log := testNote + "Started\n" + body + "unterminated"
	want := strings.SplitAfter(body, "\n")
	want = want[len(want)-11 : len(want)-1]
	for _, mode := range staplertest.Modes {
		for name, window := range map[string]int64{"small": 100, "past the cap": 1 << 20} {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				srv := newConsoleServer(t, mode)
				srv.set(log, true)
				text, err := NewClient(srv.URL, "u", "t").GetBuildLogTail("app", 1, window)
				require.NoError(t, err)
				got := strings.SplitAfter(text, "\n")
				require.GreaterOrEqual(t, len(got), 11)
				assert.Equal(t, "", got[len(got)-1], "ends at the last complete line")
				assert.Equal(t, want, got[len(got)-11:len(got)-1])
			})
		}
	}
}

func TestGetBuildLogTailComplete(t *testing.T) {
	log := "HEAD\n" + lines("c", 50) + "END\n"
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			srv := newConsoleServer(t, mode)
			srv.set(log, false)
			client := NewClient(srv.URL, "u", "t")

			text, err := client.GetBuildLogTail("app", 1, 12)
			require.NoError(t, err)
			assert.Equal(t, "c00049\nEND\n", text, "partial leading line dropped")

			text, err = client.GetBuildLogTail("app", 1, 1<<20)
			require.NoError(t, err)
			assert.Equal(t, log, text, "window larger than log returns everything untrimmed")
		})
	}
}

func TestGetBuildLogSize(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Streaming)
	srv.set(strings.Repeat("x", 1234), false)
	size, err := NewClient(srv.URL, "u", "t").GetBuildLogSize("app", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), size)
}

func TestOpenConsoleText(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Streaming)
	srv.set(testNote+"a\r\nb\npartial", true)
	body, err := NewClient(srv.URL, "u", "t").OpenConsoleText("app", 1)
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	text, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, "a\r\nb\npartial", string(text))
}
