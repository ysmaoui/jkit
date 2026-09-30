package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestNormalizeBluePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"svc", "svc"},
		{"team/svc", "team/pipelines/svc"},
		{"org/team/svc", "org/pipelines/team/pipelines/svc"},
		{"my app", "my%20app"},
		{"team/my job", "team/pipelines/my%20job"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeBluePath(tt.input))
		})
	}
}

func TestGetPipelineStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/nodes/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "10", "displayName": "Build", "result": "SUCCESS", "durationInMillis": 5000},
				{"id": "20", "displayName": "Test", "result": "FAILURE", "durationInMillis": 3000},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, stages, 2)
	assert.Equal(t, "Build", stages[0].Name)
	assert.Equal(t, "10", stages[0].ID)
	assert.Equal(t, "Test", stages[1].Name)
	assert.Equal(t, "FAILURE", stages[1].Status)
}

func TestGetPipelineStagesWithParentAndType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/nodes/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "1", "displayName": "Build", "result": "SUCCESS", "durationInMillis": 5000, "type": "STAGE"},
				{"id": "2", "displayName": "Parallel", "result": "SUCCESS", "durationInMillis": 10000, "firstParent": "1", "type": "PARALLEL"},
				{"id": "3", "displayName": "branch-a", "result": "SUCCESS", "durationInMillis": 3000, "firstParent": "2", "type": "STAGE"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, stages, 3)
	assert.Equal(t, "", stages[0].FirstParent)
	assert.Equal(t, "STAGE", stages[0].Type)
	assert.Equal(t, "1", stages[1].FirstParent)
	assert.Equal(t, "PARALLEL", stages[1].Type)
	assert.Equal(t, "2", stages[2].FirstParent)
}

func TestGetPipelineStages404ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	assert.Nil(t, stages)
}

func TestGetStageLogDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/nodes/10/log/") {
			_, _ = fmt.Fprint(w, "stage log line 1\nstage log line 2\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	log, _, err := client.GetStageLog("team/svc", 42, "10")
	require.NoError(t, err)
	assert.Contains(t, log, "stage log line 1")
	assert.Contains(t, log, "stage log line 2")
}

func TestGetStageLog404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, _, err := client.GetStageLog("team/svc", 42, "10")
	require.ErrorIs(t, err, ErrStageLogUnavailable)
}

func TestGetStageLogFallbackToSteps(t *testing.T) {
	var nodeLogCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Node-level log → return 500 to trigger fallback
		if strings.HasSuffix(path, "/nodes/10/log/") {
			atomic.AddInt32(&nodeLogCalls, 1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Steps listing
		if strings.Contains(path, "/nodes/10/steps/") && !strings.Contains(path, "/steps/101/") && !strings.Contains(path, "/steps/102/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "101", "displayName": "Shell Script"},
				{"id": "102", "displayName": "Archive artifacts"},
			})
			return
		}
		// Step 101 log
		if strings.HasSuffix(path, "/steps/101/log/") {
			_, _ = fmt.Fprint(w, "step 1 output\n")
			return
		}
		// Step 102 log
		if strings.HasSuffix(path, "/steps/102/log/") {
			_, _ = fmt.Fprint(w, "step 2 output\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	log, _, err := client.GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&nodeLogCalls))
	assert.Contains(t, log, "step 1 output")
	assert.Contains(t, log, "step 2 output")
}

func TestGetStageLogFallbackNoSteps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/log/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(path, "/steps/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, _, err := client.GetStageLog("svc", 42, "10")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no steps found")
}

func TestGetStageLogFallbackStepLogErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/nodes/10/log/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(path, "/steps/") && !strings.Contains(path, "/steps/101/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "101", "displayName": "Bad step"},
			})
			return
		}
		// Step log also fails
		if strings.HasSuffix(path, "/steps/101/log/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, _, err := client.GetStageLog("svc", 42, "10")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no log content")
}

func TestGetStageLogFallbackPartialStepLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/nodes/10/log/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(path, "/nodes/10/steps/") && !strings.Contains(path, "/steps/201/") && !strings.Contains(path, "/steps/202/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "201", "displayName": "Good step"},
				{"id": "202", "displayName": "Failed step"},
			})
			return
		}
		if strings.HasSuffix(path, "/steps/201/log/") {
			_, _ = fmt.Fprint(w, "only good output")
			return
		}
		if strings.HasSuffix(path, "/steps/202/log/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	log, _, err := client.GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Contains(t, log, "only good output")
}

// blueOceanStepThreshold mirrors LogResource.DEFAULT_LOG_THRESHOLD (150 KB):
// a step log requested without start= is cut to its last this many bytes.
const blueOceanStepThreshold = 150 << 10

// stepRequests records which step logs a stepsFallbackServer served. Handlers
// run on server goroutines, hence the lock.
type stepRequests struct {
	mu  sync.Mutex
	ids []string
}

func (s *stepRequests) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id)
}

func (s *stepRequests) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ids)
}

// stepsFallbackServer answers node 10's log with 500 and serves steps "1".."n".
// A string step is its log body, served the way Blue Ocean's LogResource does;
// an http.HandlerFunc step answers its log request itself. The steps listing
// honors start= and limit= like Blue Ocean's @PagedResponse.
func stepsFallbackServer(t *testing.T, steps ...any) (*httptest.Server, *stepRequests) {
	t.Helper()
	requested := &stepRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/nodes/10/log/"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(path, "/nodes/10/steps/"):
			start, _ := strconv.Atoi(r.URL.Query().Get("start"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			list := []map[string]any{}
			for i := start; i < len(steps) && i < start+limit; i++ {
				list = append(list, map[string]any{"id": strconv.Itoa(i + 1)})
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.Contains(path, "/nodes/10/steps/") && strings.HasSuffix(path, "/log/"):
			id := strings.TrimSuffix(path[strings.Index(path, "/steps/")+len("/steps/"):], "/log/")
			requested.add(id)
			i, _ := strconv.Atoi(id)
			switch step := steps[i-1].(type) {
			case http.HandlerFunc:
				step(w, r)
			case string:
				if r.URL.Query().Get("start") == "" && len(step) > blueOceanStepThreshold {
					step = step[len(step)-blueOceanStepThreshold:]
				}
				_, _ = fmt.Fprint(w, step)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, requested
}

func blueOceanClient(url string, opts ...ClientOption) *Client {
	return NewClient(url, "admin", "secret", append([]ClientOption{WithPipelineSource(PipelineSourceBlueOcean)}, opts...)...)
}

func TestStepsFallbackReadsWholeStepPastBlueOceanThreshold(t *testing.T) {
	step := numberedLines(30000) // ~290 KB
	require.Greater(t, len(step), blueOceanStepThreshold)
	srv, _ := stepsFallbackServer(t, step)
	defer srv.Close()

	log, truncated, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, step, log)
	assert.False(t, truncated)
}

func TestStepsFallbackStopsAtCap(t *testing.T) {
	srv, requested := stepsFallbackServer(t, numberedLines(5), numberedLines(5), numberedLines(5))
	defer srv.Close()

	log, truncated, err := blueOceanClient(srv.URL, WithStageLogCap(40)).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Equal(t, (numberedLines(5) + numberedLines(5))[:40], log)
	assert.Equal(t, []string{"1", "2"}, requested.IDs(), "steps past the cap are not fetched")
}

func TestStepsFallbackTailReturnsEnd(t *testing.T) {
	srv, _ := stepsFallbackServer(t, numberedLines(1000), "last step a\nlast step b")
	defer srv.Close()

	log, truncated, err := blueOceanClient(srv.URL, WithStageLogCap(64)).GetStageLogTail("svc", 42, "10")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(log), 64)
	assert.True(t, strings.HasSuffix(log, "line 1000\nlast step a\nlast step b\n"), "%q", log)
}

func TestStepsFallbackSeparatesUnterminatedSteps(t *testing.T) {
	srv, _ := stepsFallbackServer(t, "a", "", "b\n", "c")
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "a\nb\nc\n", log)
}

func TestStepsFallbackMarksFailedStepAndContinues(t *testing.T) {
	fail := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv, _ := stepsFallbackServer(t, "a", fail, "c\n")
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "a\n[jkit: step 2 log unavailable: HTTP 500]\nc\n", log)
}

func TestStepsFallbackMarksStepCutMidBody(t *testing.T) {
	broken := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = fmt.Fprint(w, "partial")
	})
	srv, _ := stepsFallbackServer(t, "a\n", broken, "c\n")
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "a\npartial\n[jkit: step 2 log incomplete: unexpected EOF]\nc\n", log)
}

func TestStepsFallbackServerWideErrorsFailTheRead(t *testing.T) {
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
	}
	// Dropping the connection unanswered makes the client retry, then give up
	// with UnreachableError.
	hangUp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	tests := []struct {
		name   string
		step   http.HandlerFunc
		target any
	}{
		{"unauthorized", status(http.StatusUnauthorized), new(*jenkins.AuthError)},
		{"forbidden", status(http.StatusForbidden), new(*jenkins.PermissionError)},
		{"unreachable", hangUp, new(*jenkins.UnreachableError)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requested := stepsFallbackServer(t, "a\n", tt.step, "c\n")
			defer srv.Close()

			_, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
			require.ErrorAs(t, err, tt.target)
			assert.NotContains(t, requested.IDs(), "3", "no step is read after a server-wide error")
		})
	}
}

func TestStepsFallbackPagesPastClampedLimit(t *testing.T) {
	const clamp = 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/nodes/10/log/"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(path, "/nodes/10/steps/"):
			start, _ := strconv.Atoi(r.URL.Query().Get("start"))
			list := []map[string]any{}
			for i := start; i < 5 && i < start+clamp; i++ {
				list = append(list, map[string]any{"id": strconv.Itoa(i + 1)})
			}
			_ = json.NewEncoder(w).Encode(list)
		default:
			id := strings.TrimSuffix(path[strings.Index(path, "/steps/")+len("/steps/"):], "/log/")
			_, _ = fmt.Fprintf(w, "step %s\n", id)
		}
	}))
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "step 1\nstep 2\nstep 3\nstep 4\nstep 5\n", log)
}

func TestStepsFallbackClosesOpenStepAfterCap(t *testing.T) {
	closed := make(chan struct{})
	endless := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, numberedLines(100))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(closed)
		case <-time.After(5 * time.Second):
		}
	})
	srv, _ := stepsFallbackServer(t, endless)
	defer srv.Close()

	_, truncated, err := blueOceanClient(srv.URL, WithStageLogCap(64)).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.True(t, truncated)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("step log body left open after a capped read")
	}
}

func TestStepsFallbackPagesStepListing(t *testing.T) {
	old := stepsPageSize
	stepsPageSize = 2
	defer func() { stepsPageSize = old }()
	srv, requested := stepsFallbackServer(t, "a\n", "b\n", "c\n", "d\n", "e\n")
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "a\nb\nc\nd\ne\n", log)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, requested.IDs())
}

func TestStepsFallbackStopsWhenListingIgnoresStart(t *testing.T) {
	old := stepsPageSize
	stepsPageSize = 2
	defer func() { stepsPageSize = old }()
	var listings int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/nodes/10/log/"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(path, "/nodes/10/steps/"):
			listings++
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "1"}, {"id": "2"}})
		default:
			_, _ = fmt.Fprint(w, "x\n")
		}
	}))
	defer srv.Close()

	log, _, err := blueOceanClient(srv.URL).GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, "x\nx\n", log)
	assert.Equal(t, 2, listings)
}

func TestStepsFallbackCannotBeFollowed(t *testing.T) {
	srv, requested := stepsFallbackServer(t, "a\n")
	defer srv.Close()

	var out strings.Builder
	n, _, err := blueOceanClient(srv.URL).CopyStageLogFrom("svc", 42, "10", 0, &out)
	require.ErrorIs(t, err, ErrStageLogPerStep)
	assert.Zero(t, n)
	assert.Empty(t, requested.IDs())
}

// stageLogBodyServer serves body as every PGV stage log.
func stageLogBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}))
}

// numberedLines returns "line 1\n".."line n\n".
func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		_, _ = fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestGetStageLogOverCap(t *testing.T) {
	srv := stageLogBodyServer(t, numberedLines(100))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret", WithStageLogCap(64))
	log, truncated, err := client.GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Len(t, log, 64)
	assert.True(t, strings.HasPrefix(log, "line 1\n"))
	assert.True(t, truncated)
}

func TestGetStageLogAtCapNotTruncated(t *testing.T) {
	body := numberedLines(3)
	srv := stageLogBodyServer(t, body)
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret", WithStageLogCap(len(body)))
	log, truncated, err := client.GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, body, log)
	assert.False(t, truncated)

	log, truncated, err = client.GetStageLogTail("svc", 42, "10")
	require.NoError(t, err)
	assert.Equal(t, body, log)
	assert.False(t, truncated)
}

func TestGetStageLogTailReturnsEnd(t *testing.T) {
	srv := stageLogBodyServer(t, numberedLines(100000))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret", WithStageLogCap(64))
	log, truncated, err := client.GetStageLogTail("svc", 42, "10")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(log), 64)
	assert.True(t, strings.HasPrefix(log, "line "), "partial first line dropped: %q", log)
	assert.True(t, strings.HasSuffix(log, "line 99999\nline 100000\n"))
}

func TestGetStageLogTailKeepsWindowEndingInOnlyNewline(t *testing.T) {
	srv := stageLogBodyServer(t, strings.Repeat("x", 200)+"\n")
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret", WithStageLogCap(64))
	log, truncated, err := client.GetStageLogTail("svc", 42, "10")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Equal(t, strings.Repeat("x", 63)+"\n", log)
}

func TestGetStageLogTailKeepsPartialOnReadError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, numberedLines(50))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := NewClient(srv.URL, "admin", "secret", WithTimeout(200*time.Millisecond), WithStageLogCap(64))
	log, truncated, err := client.GetStageLogTail("svc", 42, "10")
	var ne net.Error
	require.ErrorAs(t, err, &ne)
	assert.True(t, ne.Timeout())
	assert.True(t, truncated)
	assert.True(t, strings.HasPrefix(log, "line "), "partial first line dropped: %q", log)
	assert.True(t, strings.HasSuffix(log, "line 49\nline 50\n"), log)
}

func TestCopyStageLogFromOffset(t *testing.T) {
	srv := stageLogBodyServer(t, numberedLines(3))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	var out strings.Builder
	n, capped, err := client.CopyStageLogFrom("svc", 42, "10", int64(len("line 1\n")), &out)
	require.NoError(t, err)
	assert.Equal(t, "line 2\nline 3\n", out.String())
	assert.Equal(t, int64(out.Len()), n)
	assert.False(t, capped)

	out.Reset()
	n, capped, err = client.CopyStageLogFrom("svc", 42, "10", 1000, &out)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.False(t, capped)
	assert.Empty(t, out.String())
}

func TestCopyStageLogFromStopsAtCap(t *testing.T) {
	body := numberedLines(100)
	srv := stageLogBodyServer(t, body)
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret", WithStageLogCap(64))
	var out strings.Builder
	n, capped, err := client.CopyStageLogFrom("svc", 42, "10", 20, &out)
	require.NoError(t, err)
	assert.True(t, capped)
	assert.Equal(t, int64(44), n)
	assert.Equal(t, body[20:64], out.String())
}

func TestCopyStageLogFromSkipsPGVPlaceholder(t *testing.T) {
	srv := stageLogBodyServer(t, "No logs found\n")
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	var out strings.Builder
	n, capped, err := client.CopyStageLogFrom("svc", 42, "10", 0, &out)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.False(t, capped)
	assert.Empty(t, out.String())
}
