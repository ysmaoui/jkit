package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// waitServer serves build #42 of my-app. Each build poll advances one step;
// builds and deployStates are indexed by step, their last entry repeating.
// Stage polls return the PGV state of a "Deploy" stage, absent for "".
func waitServer(t *testing.T, builds []map[string]any, deployStates []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	step := -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/job/my-app/42/api/json" && strings.Contains(r.URL.Query().Get("tree"), "waitingForInput"):
			_ = json.NewEncoder(w).Encode(map[string]any{"actions": []any{}})
		case r.URL.Path == "/job/my-app/42/api/json":
			step++
			_ = json.NewEncoder(w).Encode(builds[min(step, len(builds)-1)])
		case r.URL.Path == "/job/my-app/42/stages/tree":
			stages := []map[string]any{{"id": "7", "name": "Build", "type": "STAGE", "state": "success"}}
			if s := deployStates[min(step, len(deployStates)-1)]; s != "" {
				stages = append(stages, map[string]any{"id": "9", "name": "Deploy", "type": "STAGE", "state": s, "totalDurationMillis": 3000})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"stages": stages}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
	return srv
}

func fastWaitPolls(t *testing.T) {
	old := waitPollInterval
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = old })
}

var (
	running = map[string]any{"number": 42, "building": true}
	done    = func(result string) map[string]any {
		return map[string]any{"number": 42, "building": false, "result": result, "duration": 5000}
	}
)

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *jenkins.ExitError
	require.True(t, errors.As(err, &ee), "want ExitError, got %v", err)
	return ee.Code
}

func TestWaitBuildExitCodes(t *testing.T) {
	for result, want := range map[string]int{"SUCCESS": 0, "FAILURE": 1, "UNSTABLE": 2, "ABORTED": 3} {
		t.Run(result, func(t *testing.T) {
			fastWaitPolls(t)
			waitServer(t, []map[string]any{running, running, done(result)}, []string{""})

			out, err := executeCmd(t, "wait", "my-app", "42")
			assert.Equal(t, want, exitCode(t, err))
			assert.Contains(t, out, "my-app #42: "+result)
		})
	}
}

func TestWaitFinishedBuildReturnsWithoutPolling(t *testing.T) {
	old := waitPollInterval
	waitPollInterval = time.Hour
	t.Cleanup(func() { waitPollInterval = old })
	waitServer(t, []map[string]any{done("SUCCESS")}, []string{""})

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "wait", "my-app", "42") })
	require.NoError(t, err)
	assert.NotContains(t, stderr, "Waiting for")
}

func TestWaitStageSuccessWhileBuildRuns(t *testing.T) {
	fastWaitPolls(t)
	// Build never finishes: the stage result alone must end the wait.
	waitServer(t, []map[string]any{running}, []string{"", "running", "success"})

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "wait", "my-app", "42", "--stage", "Deploy") })
	require.NoError(t, err)
	assert.Contains(t, stderr, `Waiting for stage "Deploy" in build #42 of my-app`)
	assert.Contains(t, out, `my-app #42 stage "Deploy": SUCCESS (3s)`)
}

func TestWaitStageFailedByID(t *testing.T) {
	fastWaitPolls(t)
	waitServer(t, []map[string]any{running}, []string{"running", "failure"})

	out, err := executeCmd(t, "wait", "my-app", "42", "--stage", "9", "--json")
	assert.Equal(t, 1, exitCode(t, err))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, map[string]any{"job": "my-app", "build": float64(42), "stage": "Deploy", "result": "FAILURE", "durationMillis": float64(3000)}, got)
}

func TestWaitStageNeverRan(t *testing.T) {
	fastWaitPolls(t)
	waitServer(t, []map[string]any{running, done("FAILURE")}, []string{""})

	_, err := executeCmd(t, "wait", "my-app", "42", "--stage", "Deploy")
	assert.Equal(t, 4, exitCode(t, err))
	assert.Contains(t, err.Error(), `stage "Deploy" never ran — build #42 finished FAILURE`)
}

func TestWaitStageSkipped(t *testing.T) {
	fastWaitPolls(t)
	// not_built means "not started" until the build ends, then "skipped".
	waitServer(t, []map[string]any{running, done("SUCCESS")}, []string{"not_built"})

	out, err := executeCmd(t, "wait", "my-app", "42", "--stage", "Deploy")
	assert.Equal(t, 4, exitCode(t, err))
	assert.Contains(t, out, "NOT_BUILT")
	assert.Contains(t, err.Error(), "has no result")
}

func TestWaitAmbiguousStageNamesNoLogFlag(t *testing.T) {
	stages := parallelStagesServer(t, "running", "running")
	defer stages.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/job/my-app/5/api/json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": true})
			return
		}
		http.Redirect(w, r, stages.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "wait", "my-app", "5", "--stage", "Run Bazel Build")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), `pass a qualified path (e.g. "RemoteExec/Run Bazel Build") or the stage ID`)
	assert.NotContains(t, err.Error(), "--stage-id")
}

// The server stalls past --max-wait. Without the global 20ms HTTP timeout the
// first request is still pending when --max-wait fires; with it, the request
// times out and the client retries after its 500ms backoff.
func TestWaitKeepsGlobalHTTPTimeout(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "wait", "my-app", "42", "--timeout", "20ms", "--max-wait", "900ms")
	assert.Equal(t, 5, exitCode(t, err))
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, requests, 2, "a retry means the 20ms HTTP timeout fired")
}

func TestWaitStageWithoutStageDataFailsFast(t *testing.T) {
	fastWaitPolls(t)
	// Neither /stages/tree nor Blue Ocean exists: GetPipelineStages is nil, nil.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/job/my-app/42/api/json" {
			_ = json.NewEncoder(w).Encode(running)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "wait", "my-app", "42", "--stage", "Deploy", "--max-wait", "5s")
	require.Error(t, err)
	assert.Equal(t, "no stages for build #42 of my-app — --stage needs a pipeline job and the Pipeline Graph View or Blue Ocean plugin", err.Error())
}

func TestWaitMaxWait(t *testing.T) {
	fastWaitPolls(t)
	waitServer(t, []map[string]any{running}, []string{""})

	start := time.Now()
	out, err := executeCmd(t, "wait", "my-app", "42", "--max-wait", "5ms")
	assert.Equal(t, 5, exitCode(t, err))
	assert.Contains(t, err.Error(), "gave up after 5ms (--max-wait) waiting for build #42 of my-app")
	assert.Empty(t, out)
	assert.Less(t, time.Since(start), 5*time.Second)
}
