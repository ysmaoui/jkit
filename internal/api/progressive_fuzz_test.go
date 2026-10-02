package api

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

// fuzzLog builds a log from pieces that each stress where a running log's
// text stops: unterminated lines, lone CRs, CRLFs, console notes and bytes
// Jenkins cannot decode, and characters a cut can split.
func fuzzLog(r *rand.Rand, n int) string {
	pieces := []string{
		".\n", ".\n", ".\n", "ab\n", "x", "\r\n", "10%\r", "line\n", "\n",
		testNote, testNote + "[Pipeline] sh\n", "\xe9", "\xe9\n", "caf\u00e9", "\u20ac\u20ac\n",
	}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(pieces[r.Intn(len(pieces))])
	}
	return b.String()
}

// growingServer serves a log that grows by up to 60 bytes at every
// progressiveText request and completes at a set request. Overrun and Uncounted
// answers are sized before the growth and read it, Uncounted cut at the size.
// sent counts the bytes it wrote.
type growingServer struct {
	*httptest.Server
	mu         sync.Mutex
	final      string
	pos        int
	requests   int
	completeAt int
	sent       int64
}

type countingWriter struct {
	http.ResponseWriter
	n *int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	*c.n += int64(len(p))
	return c.ResponseWriter.Write(p)
}

func newGrowingServer(t *testing.T, mode staplertest.Mode, r *rand.Rand, final string) *growingServer {
	t.Helper()
	s := &growingServer{final: final, completeAt: 1 + r.Intn(len(final)/10+2)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if req.URL.Path == "/api/json" {
			staplertest.WriteRoot(w, mode)
			return
		}
		s.requests++
		before := s.final[:s.pos]
		s.pos = min(s.pos+r.Intn(61), len(s.final))
		open := s.requests < s.completeAt
		if !open {
			s.pos = len(s.final)
		}
		log := s.final[:s.pos]
		if (mode == staplertest.Overrun || mode == staplertest.Uncounted) && open {
			staplertest.WriteProgressiveDuring(countingWriter{w, &s.sent}, req, mode, before, log, open)
			return
		}
		staplertest.WriteProgressive(countingWriter{w, &s.sent}, req, mode, log, open)
	}))
	t.Cleanup(s.Close)
	return s
}

// TestConsoleLogFollowFuzz follows random logs that grow between and during
// requests, in every mode. Each poll must write a prefix of the final text,
// none may stall (no log here reaches the line cap), the follow must end with
// all of it, and the answers together may hold the log only a few times
// over. JKIT_FUZZ_SEEDS sets how many logs; the default keeps the run short.
func TestConsoleLogFollowFuzz(t *testing.T) {
	seeds := 40
	if n, err := strconv.Atoi(os.Getenv("JKIT_FUZZ_SEEDS")); err == nil {
		seeds = n
	}
	for seed := range seeds {
		for _, mode := range staplertest.Modes {
			r := rand.New(rand.NewSource(int64(seed)))
			final := fuzzLog(r, 50+r.Intn(3000))
			srv := newGrowingServer(t, mode, r, final)
			l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
			want := staplertest.Decode(shown(mode, final))
			var out strings.Builder
			for poll := 0; ; poll++ {
				more, err := l.Read(context.Background(), &out)
				if err != nil {
					t.Fatalf("seed %d %s, poll %d: %v", seed, mode, poll, err)
				}
				if !strings.HasPrefix(want, out.String()) {
					t.Fatalf("seed %d %s, poll %d: wrote %q, not a prefix of the final text", seed, mode, poll, tailOf(out.String()))
				}
				if l.Stalled() {
					t.Fatalf("seed %d %s, poll %d: stalled", seed, mode, poll)
				}
				if !more {
					break
				}
				if poll > 10*len(final) {
					t.Fatalf("seed %d %s: never completed", seed, mode)
				}
			}
			if out.String() != want {
				t.Fatalf("seed %d %s: wrote %q, want %q", seed, mode, tailOf(out.String()), tailOf(want))
			}
			// A follow that re-read from the first line start at every poll
			// sends about polls/2 times the log, here up to a few hundred.
			// Plain answers after 2.508 can be placed only when a poll lands
			// on a line end, which a log growing at every request rarely
			// does, so they re-read that much too and are left out.
			limit := 6*int64(len(final)) + 512*int64(srv.requests)
			if (mode == staplertest.Streaming || mode == staplertest.Counted) && srv.sent > limit {
				t.Fatalf("seed %d %s: answers sent %d bytes for a %d-byte log in %d requests", seed, mode, srv.sent, len(final), srv.requests)
			}
			srv.Close()
		}
	}
}

func tailOf(s string) string { return s[max(len(s)-80, 0):] }
