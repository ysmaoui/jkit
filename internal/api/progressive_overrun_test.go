package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

func TestOverrunsTextSize(t *testing.T) {
	for version, want := range map[string]bool{
		"2.508": false, "2.509": true, "2.516.3": true, "2.526": true,
		"2.527": false, "2.528.3": false, "2.568.3": false, "": true,
	} {
		assert.Equal(t, want, overrunsTextSize(version), version)
	}
}

// TestConsoleLogFollowOverrun follows a log that grows during every answer on
// Stapler 1979-2028, whose plain body then runs past X-Text-Size. Such an
// answer is no line cap: the follow must neither stall nor write a line
// twice. A CRLF received may be a stored LF, so the stored CRLFs a build wrote
// hide that much growth from the body; only the size checked after the
// answer shows it.
func TestConsoleLogFollowOverrun(t *testing.T) {
	for name, tc := range map[string]struct{ start, growth string }{
		"lines":                 {"start\n", "line with a CRLF\r\nplain line\n"},
		"hidden by stored CRLF": {strings.Repeat("windows\r\n", 10), "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			log := tc.start
			open := true
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/api/json" {
					staplertest.WriteRoot(w, staplertest.Overrun)
					return
				}
				before := log
				if open {
					log += tc.growth
				}
				staplertest.WriteProgressiveDuring(w, r, staplertest.Overrun, before, log, open)
			}))
			defer srv.Close()

			l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
			var out strings.Builder
			for range 20 {
				more, err := l.Read(context.Background(), &out)
				require.NoError(t, err)
				require.True(t, more)
				require.False(t, l.Stalled(), "a body past the size is not the line cap")
				mu.Lock()
				want := strings.ReplaceAll(log, "\r\n", "\n")
				mu.Unlock()
				require.True(t, strings.HasPrefix(want, out.String()), "wrote a line twice")
			}
			mu.Lock()
			open = false
			final := log
			mu.Unlock()
			more, err := l.Read(context.Background(), &out)
			require.NoError(t, err)
			assert.False(t, more)
			assert.Equal(t, strings.ReplaceAll(final, "\r\n", "\n"), out.String())
		})
	}
}

// TestPlainAnswerUndecodableBytesKeepCandidate checks that an undecodable
// byte, which Jenkins sends as the three bytes of U+FFFD, is not taken for a
// body running past its size: the stored length the answer reported stays the
// candidate for placing the next poll.
func TestPlainAnswerUndecodableBytesKeepCandidate(t *testing.T) {
	srv := newConsoleServer(t, staplertest.Uncounted)
	log := "caf\xe9\n"
	srv.set(log, true)
	l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
	var out strings.Builder
	_, err := l.Read(context.Background(), &out)
	require.NoError(t, err)
	assert.Equal(t, "caf\uFFFD\n", out.String())
	assert.Equal(t, int64(len(log)), l.candidate)
}
