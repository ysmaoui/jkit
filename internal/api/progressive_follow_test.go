package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

type pollState struct {
	log  string
	open bool
}

// follow reads l once per state and checks that what it wrote is always a
// prefix of want, and want itself once the log completes.
func follow(t *testing.T, srv *consoleServer, l *ProgressiveLog, states []pollState, want string) {
	t.Helper()
	var out strings.Builder
	for i, p := range states {
		srv.set(p.log, p.open)
		more, err := l.Read(context.Background(), &out)
		require.NoError(t, err, "poll %d", i)
		assert.Equal(t, p.open, more, "poll %d", i)
		require.True(t, strings.HasPrefix(want, out.String()), "poll %d wrote %q", i, out.String()[max(out.Len()-80, 0):])
	}
	assert.Equal(t, want, out.String())
}

// TestConsoleLogFollowSplitCR follows a CRLF split across polls and a lone CR
// at a poll's end. A plain answer for a running log stops after either, and
// asked from the byte after it would turn the LF into CRLF.
func TestConsoleLogFollowSplitCR(t *testing.T) {
	s0 := "a\nxyz\r"
	s1 := s0 + "\nnext\n50%\r"
	s2 := s1 + "60%\nend\n"
	states := []pollState{{s0, true}, {s0, true}, {s1, true}, {s1, true}, {s2, false}}
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			srv := newConsoleServer(t, mode)
			follow(t, srv, NewClient(srv.URL, "u", "t").ConsoleLog("app", 1), states, shown(mode, s2))
		})
	}
}

// TestConsoleLogFollowCRBurst follows a burst of progress-bar lines. Stapler
// caps a plain answer at MaxLinesRead line ends counting each CR, so fewer LF
// lines than the cap still fill it.
func TestConsoleLogFollowCRBurst(t *testing.T) {
	s0 := "start\n"
	s1 := s0 + strings.Repeat("10%\r20%\r30%\n", staplertest.MaxLinesRead/2)
	s2 := s1 + "tail\n"
	s3 := s2 + "end\n"
	states := []pollState{{s0, true}, {s1, true}, {s2, true}, {s3, false}}
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			srv := newConsoleServer(t, mode)
			follow(t, srv, NewClient(srv.URL, "u", "t").ConsoleLog("app", 1), states, shown(mode, s3))
		})
	}
}

// TestConsoleLogFollowChattyReadsLittle follows a log that each poll finds
// mid-line. A multipart server lets every poll start near where the last one
// stopped, so the answers together hold the log a few times over, not once
// per poll as when every poll re-reads from the first line start (about 60
// times here). A console note between polls, as a pipeline writes at each
// step, costs a few more searches each.
func TestConsoleLogFollowChattyReadsLittle(t *testing.T) {
	for name, tc := range map[string]struct {
		noteEvery int
		maxRatio  int
	}{"no notes": {0, 3}, "a note every poll": {50, 16}} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			for i := range 6000 {
				if tc.noteEvery > 0 && i%tc.noteEvery == 0 {
					b.WriteString(testNote + "[Pipeline] sh\n")
				}
				fmt.Fprintf(&b, "output line %05d of a chatty step\n", i)
			}
			final := b.String()
			var states []pollState
			for cut := 1000; cut < len(final); cut += 1777 {
				states = append(states, pollState{final[:cut], true})
			}
			states = append(states, pollState{final, false})

			srv := newConsoleServer(t, staplertest.Streaming)
			follow(t, srv, NewClient(srv.URL, "u", "t").ConsoleLog("app", 1), states, shown(staplertest.Streaming, final))
			assert.Less(t, srv.behind, int64(tc.maxRatio*len(final)), "polls re-read the log")
		})
	}
}

// TestConsoleLogFollowLateJoinReadsLittle joins a running log of 1.4 MB with a
// console note on its first line, then finds it mid-line at every poll. The
// note puts the first poll's search off by its length; the anchor then moves
// past it and stays near the end.
func TestConsoleLogFollowLateJoinReadsLittle(t *testing.T) {
	var b strings.Builder
	b.WriteString(testNote + "Started by user admin\n")
	for i := range 40000 {
		fmt.Fprintf(&b, "output line %05d of a chatty step\n", i)
	}
	final := b.String()
	joined := len(final) / 2
	var states []pollState
	for cut := joined; cut < len(final); cut += 1777 {
		states = append(states, pollState{final[:cut], true})
	}
	states = append(states, pollState{final, false})

	srv := newConsoleServer(t, staplertest.Streaming)
	follow(t, srv, NewClient(srv.URL, "u", "t").ConsoleLog("app", 1), states, shown(staplertest.Streaming, final))
	assert.Less(t, srv.behind, int64(3*len(final)), "polls re-read the log")
}

// TestConsoleTailLinesStreamingOneRequest asks a multipart server for the
// tail with a negative start, and nothing else.
func TestConsoleTailLinesStreamingOneRequest(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Streaming)
	srv.set(testNote+"Started\n"+lines("t", 20000)+"unterminated", true)
	got, err := NewClient(srv.URL, "u", "t").ConsoleTailLines("app", 1, 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"t19997", "t19998", "t19999"}, got)
	assert.Equal(t, []int64{-startTailWindow}, srv.takeStarts())
}

// TestConsoleTailLinesStalledShrinks covers Jenkins 2.509-2.533 on a running
// log whose tail window holds more lines than a plain answer: the window is
// halved until one answer holds it, rather than reading consoleText.
func TestConsoleTailLinesStalledShrinks(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Uncounted)
	srv.set(lines("s", 3*staplertest.MaxLinesRead)+"unterminated", true)
	var consoleText bool
	h := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		consoleText = consoleText || strings.HasSuffix(r.URL.Path, "/consoleText")
		h.ServeHTTP(w, r)
	})
	got, err := NewClient(srv.URL, "u", "t").ConsoleTailLines("app", 1, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"s29998", "s29999"}, got)
	assert.False(t, consoleText)
}

// TestServerVersionRetriedAfterFailure checks a failed version probe is not
// remembered as "no version".
func TestServerVersionRetriedAfterFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		staplertest.WriteRoot(w, staplertest.Counted)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "t")
	assert.Equal(t, "", c.serverVersion(context.Background()))
	assert.Equal(t, "2.479.3", c.serverVersion(context.Background()))
	assert.Equal(t, "2.479.3", c.serverVersion(context.Background()))
	assert.Equal(t, 2, calls)
}
