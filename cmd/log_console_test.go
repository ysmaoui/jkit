package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

// consoleServer serves build 5 of my-app as Jenkins in mode would. Each
// progressiveText request moves the console one state on, so a follow sees
// it grow whatever its request pattern. The last state is complete unless
// the build runs on.
type consoleServer struct {
	*httptest.Server
	mu     sync.Mutex
	states []string
	state  int
	runsOn bool
}

func newConsoleServer(t *testing.T, mode staplertest.Mode, runsOn bool, states ...string) *consoleServer {
	t.Helper()
	s := &consoleServer{states: states, runsOn: runsOn}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		log, open := s.states[s.state], s.runsOn || s.state < len(s.states)-1
		switch r.URL.Path {
		case "/api/json":
			staplertest.WriteRoot(w, mode)
		case "/job/my-app/5/api/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": open})
		case "/job/my-app/5/logText/progressiveText":
			staplertest.WriteProgressive(w, r, mode, log, open)
			s.state = min(s.state+1, len(s.states)-1)
		case "/job/my-app/5/consoleText":
			staplertest.ConsoleText(w, log)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	setupTestConfig(t, s.URL)
	return s
}

const consoleNote = "\x1b[8mha:////NOTE\x1b[0m"

func numbered(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%s%05d\n", prefix, i)
	}
	return b.String()
}

// TestLogFollowConsole follows a console that gains a note, an unterminated
// line, a CRLF the build wrote and a burst past the line cap of a plain
// answer. Every byte must be printed once. Plain answers cannot carry the
// build's CR before LF, so there it prints as LF.
func TestLogFollowConsole(t *testing.T) {
	old := consolePollInterval
	consolePollInterval = time.Millisecond
	t.Cleanup(func() { consolePollInterval = old })

	s0 := consoleNote + "Started\nl1\n"
	s1 := s0 + "l2\npart"
	s2 := s1 + "ial\nl3\r\nl4\n"
	s3 := s2 + numbered("b", 0, staplertest.MaxLinesRead+5)
	s4 := s3 + "Finished\n"
	printed := strings.TrimPrefix(s4, consoleNote)

	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			newConsoleServer(t, mode, false, s0, s0, s1, s1, s2, s2, s3, s3, s3, s4)

			var out string
			var err error
			stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "-f") })
			require.NoError(t, err)
			want := printed
			if mode != staplertest.Streaming {
				want = strings.ReplaceAll(want, "\r\n", "\n")
			}
			assert.Equal(t, want, out)
			if mode == staplertest.Uncounted || mode == staplertest.Overrun {
				assert.Contains(t, stderr, "the rest is shown when the log completes")
			} else {
				assert.Empty(t, stderr)
			}
		})
	}
}

// TestLogTailRunningConsole asks a running console for its last lines, past
// the first 10000 and across that boundary, in every mode.
func TestLogTailRunningConsole(t *testing.T) {
	body := numbered("t", 0, staplertest.MaxLinesRead+2000)
	log := consoleNote + "Started\n" + body + "unterminated"
	all := strings.SplitAfter(body, "\n")
	all = all[:len(all)-1]
	for _, mode := range staplertest.Modes {
		for _, n := range []int{50, 2500} {
			t.Run(fmt.Sprintf("%s/%d", mode, n), func(t *testing.T) {
				newConsoleServer(t, mode, true, log)
				out, err := executeCmd(t, "log", "my-app", "5", "--tail", fmt.Sprint(n))
				require.NoError(t, err)
				assert.Equal(t, strings.Join(all[len(all)-n:], ""), out)
			})
		}
	}
}

// TestLogGrepRunningConsole reads a running console as it stands, its
// unterminated last line included, with notes stripped.
func TestLogGrepRunningConsole(t *testing.T) {
	log := consoleNote + "Started\n" + numbered("g", 0, staplertest.MaxLinesRead+5) + "g-unterminated"
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			newConsoleServer(t, mode, true, log)
			out, err := executeCmd(t, "log", "my-app", "5", "--grep", "g1000")
			require.NoError(t, err)
			assert.Equal(t, numbered("g", 10000, 10005), out)

			out, err = executeCmd(t, "log", "my-app", "5", "--grep", "unterminated")
			require.NoError(t, err)
			assert.Equal(t, "g-unterminated\n", out)

			out, err = executeCmd(t, "log", "my-app", "5", "--head", "2")
			require.NoError(t, err)
			assert.Equal(t, "Started\ng00000\n", out)
		})
	}
}
