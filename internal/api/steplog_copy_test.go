package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/staplertest"
)

const stepLogPath = "/job/svc/5/execution/node/7/log/logText/progressiveText"

func stepLogServer(t *testing.T, mode staplertest.Mode, log string, open bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/json":
			staplertest.WriteRoot(w, mode)
		case stepLogPath:
			staplertest.WriteProgressive(w, r, mode, log, open)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// copyStepLogWithin fails the test instead of hanging when the copy does not
// return.
func copyStepLogWithin(t *testing.T, c *Client, w io.Writer) (n int64, partial bool, err error) {
	t.Helper()
	type result struct {
		n       int64
		partial bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		n, partial, err := c.CopyStepLog(context.Background(), "svc", 5, "7", w)
		done <- result{n, partial, err}
	}()
	select {
	case r := <-done:
		return r.n, r.partial, r.err
	case <-time.After(30 * time.Second):
		t.Fatal("CopyStepLog did not return")
		return 0, false, nil
	}
}

type hashWriter struct {
	h hash.Hash
	n int64
}

func (h *hashWriter) Write(p []byte) (int, error) {
	h.n += int64(len(p))
	return h.h.Write(p)
}

func TestCopyStepLogWholeLargeLog(t *testing.T) {
	log := lines(strings.Repeat("x", 56), 50<<20/64)
	want := sha256.Sum256([]byte(log))
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			srv := stepLogServer(t, mode, log, false)
			hw := &hashWriter{h: sha256.New()}
			n, partial, err := copyStepLogWithin(t, NewClient(srv.URL, "u", "t"), hw)
			require.NoError(t, err)
			assert.Equal(t, int64(len(log)), n)
			assert.False(t, partial)
			assert.Equal(t, n, hw.n)
			assert.Equal(t, want[:], hw.h.Sum(nil))
		})
	}
}

func TestCopyStepLogRunningStopsAtLastLineEnd(t *testing.T) {
	cases := []struct {
		mode  staplertest.Mode
		lines int
	}{
		{staplertest.Streaming, 25000},
		// Counted answers stop at the line cap with an exact offset, so the
		// copy reads on past it.
		{staplertest.Counted, 25000},
		{staplertest.Uncounted, 3000},
		{staplertest.Overrun, 3000},
	}
	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			whole := lines("step ", tc.lines)
			srv := stepLogServer(t, tc.mode, whole+"unterminated", true)
			var out bytes.Buffer
			n, partial, err := copyStepLogWithin(t, NewClient(srv.URL, "u", "t"), &out)
			require.NoError(t, err)
			assert.Equal(t, int64(len(whole)), n)
			assert.False(t, partial)
			assert.Equal(t, whole, out.String())
		})
	}
}

func TestCopyStepLogRunningPastLineCapStops(t *testing.T) {
	whole := lines("step ", 25000)
	srv := stepLogServer(t, staplertest.Uncounted, whole, true)
	var out bytes.Buffer
	n, partial, err := copyStepLogWithin(t, NewClient(srv.URL, "u", "t"), &out)
	require.NoError(t, err)
	assert.Equal(t, lines("step ", staplertest.MaxLinesRead), out.String())
	assert.Equal(t, int64(out.Len()), n)
	assert.True(t, partial)
}

func TestCopyStepLogConsoleNotes(t *testing.T) {
	note := "\x1b[8mha:AAAA\x1b[0m"
	log := note + "[Pipeline] sh\n+ make\n"
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			srv := stepLogServer(t, mode, log, false)
			var out bytes.Buffer
			_, _, err := copyStepLogWithin(t, NewClient(srv.URL, "u", "t"), &out)
			require.NoError(t, err)
			if mode == staplertest.Streaming {
				assert.Equal(t, "[Pipeline] sh\n+ make\n", out.String())
			} else {
				assert.Equal(t, log, out.String())
			}
		})
	}
}

func TestCopyStepLogWithoutLog(t *testing.T) {
	for _, served := range []bool{true, false} {
		t.Run(fmt.Sprintf("node served %v", served), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if served && r.URL.Path == "/job/svc/5/execution/node/7/" {
					_, _ = io.WriteString(w, "<html>flow node</html>")
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			var out bytes.Buffer
			n, partial, err := NewClient(srv.URL, "u", "t").CopyStepLog(context.Background(), "svc", 5, "7", &out)
			assert.Zero(t, n)
			assert.False(t, partial)
			if served {
				require.NoError(t, err)
				return
			}
			var nfe *jenkins.NotFoundError
			assert.ErrorAs(t, err, &nfe)
		})
	}
}

func TestStepExceptionText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/job/svc/5/stages/exceptionText" && r.URL.Query().Get("nodeId") == "7" {
			_, _ = io.WriteString(w, "script returned exit code 2")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	text, err := NewClient(srv.URL, "u", "t").StepExceptionText(context.Background(), "svc", 5, "7")
	require.NoError(t, err)
	assert.Equal(t, "script returned exit code 2", text)
}

func TestStepExceptionText404(t *testing.T) {
	t.Run("build exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/job/svc/5/api/json" {
				_, _ = io.WriteString(w, `{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowRun"}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "u", "t").StepExceptionText(context.Background(), "svc", 5, "7")
		var pme *jenkins.PluginMissingError
		require.ErrorAs(t, err, &pme)
		assert.Equal(t, "Pipeline Graph View", pme.Plugin)
		assert.Equal(t, "stages/exceptionText", pme.Route)
		assert.Contains(t, err.Error(), "Pipeline Graph View")
	})
	t.Run("job missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "u", "t").StepExceptionText(context.Background(), "svc", 5, "7")
		var pme *jenkins.PluginMissingError
		assert.False(t, errors.As(err, &pme))
		var nfe *jenkins.NotFoundError
		require.ErrorAs(t, err, &nfe)
		assert.Equal(t, "job", nfe.Resource)
	})
}
