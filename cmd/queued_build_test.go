package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/staplertest"
	"github.com/ysmaoui/jkit/internal/waiter"
)

// queueServer serves job my-app, whose next build number is next, with build
// #42 in the queue for the first queuedPolls build reads and then finished.
// Any other build number 404s, and so does every route of any other job.
func queueServer(t *testing.T, next, queuedPolls int) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	buildReads := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		jobURL := srv.URL + "/job/my-app/"
		queued := buildReads < queuedPolls
		switch r.URL.Path {
		case "/job/my-app/42/api/json":
			if strings.Contains(r.URL.Query().Get("tree"), "waitingForInput") {
				_ = json.NewEncoder(w).Encode(map[string]any{"actions": []any{}})
				return
			}
			buildReads++
			if queued {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(done("SUCCESS"))
		case "/api/json":
			staplertest.WriteRoot(w, staplertest.Streaming)
		case "/job/my-app/42/logText/progressiveText":
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "console line\n", false)
		case "/job/my-app/42/consoleText":
			staplertest.ConsoleText(w, "console line\n")
		case "/job/my-app/api/json":
			n := next
			if !queued {
				n++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "my-app", "url": jobURL, "nextBuildNumber": n})
		case "/queue/api/json":
			items := []map[string]any{{"id": 7, "task": map[string]any{"name": "other", "url": srv.URL + "/job/other/"}}}
			if queued {
				items = append(items, map[string]any{"id": 8, "why": "In the quiet period", "task": map[string]any{"name": "my-app", "url": jobURL}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
	return srv, &buildReads
}

func TestWaitQueuedBuildWaitsForItToStart(t *testing.T) {
	fastWaitPolls(t)
	_, buildReads := queueServer(t, 42, 3)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "wait", "my-app", "42") })
	require.NoError(t, err)
	assert.Contains(t, out, "my-app #42: SUCCESS")
	assert.Equal(t, 1, strings.Count(stderr, "Build #42 of my-app has not started yet"), stderr)
	assert.Equal(t, 4, *buildReads)
}

// One queued item can take only the next number, #41, so #42 is not coming.
func TestWaitNumberPastQueueFails(t *testing.T) {
	fastWaitPolls(t)
	queueServer(t, 41, 2)

	_, err := executeCmd(t, "wait", "my-app", "42", "--max-wait", "5s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no queued build will get that number (next build is #41, 1 queued)")
}

func TestWaitFarFutureBuildFailsAtOnce(t *testing.T) {
	fastWaitPolls(t)
	_, buildReads := queueServer(t, 6, 1)

	_, err := executeCmd(t, "wait", "my-app", "99", "--max-wait", "5s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build #99 of my-app does not exist and no queued build will get that number (next build is #6, 1 queued)")
	assert.Equal(t, 0, *buildReads, "#99 is never read as #42")
}

func TestWaitMissingJobFailsAtOnce(t *testing.T) {
	fastWaitPolls(t)
	queueServer(t, 42, 0)

	_, err := executeCmd(t, "wait", "nope", "1", "--max-wait", "5s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `job "nope" not found`)
}

func TestWaitQueuedBuildHonorsMaxWait(t *testing.T) {
	fastWaitPolls(t)
	queueServer(t, 42, 1<<30)

	_, err := executeCmd(t, "wait", "my-app", "42", "--max-wait", "20ms")
	assert.Equal(t, 5, exitCode(t, err))
}

// emptyStagesServer serves build #5 with an empty PGV stage list.
func emptyStagesServer(t *testing.T, build map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/job/my-app/5/stages/tree":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"stages": []any{}}})
		case "/job/my-app/5/api/json":
			_ = json.NewEncoder(w).Encode(build)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
}

func TestStagesEmptyOnFinishedBuildFails(t *testing.T) {
	emptyStagesServer(t, map[string]any{"number": 5, "building": false, "result": "FAILURE"})

	_, err := executeCmd(t, "stages", "my-app", "5")
	require.Error(t, err)
	assert.Equal(t, "no stages — build #5 finished FAILURE without entering a stage", err.Error())
}

func TestStagesQueuedBuild(t *testing.T) {
	queueServer(t, 42, 1<<30)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "stages", "my-app", "42") })
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "build #42 is queued")
	assert.NotContains(t, stderr, "jkit list")

	out, err = executeCmd(t, "stages", "my-app", "42", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, "[]", out)
}

func TestLogFollowWaitsForQueuedBuild(t *testing.T) {
	fastWaitPolls(t)
	_, buildReads := queueServer(t, 42, 3)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "42", "-f") })
	require.NoError(t, err)
	assert.Equal(t, "console line\n", out)
	assert.Equal(t, 1, strings.Count(stderr, "note: build #42 is queued; waiting for it to start"), stderr)
	assert.GreaterOrEqual(t, *buildReads, 4)
}

func TestAwaitBuildStartReturnsOnCancel(t *testing.T) {
	fastWaitPolls(t)
	srv, _ := queueServer(t, 42, 1<<30)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		build, err := awaitBuildStart(ctx, api.NewClient(srv.URL, "u", "t"), "my-app", 42, waiter.Queued, io.Discard)
		if build != nil {
			err = errors.New("build returned for a queued build")
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("awaitBuildStart did not return after cancel")
	}
}

// Without -f, or with --grep which reads once, a queued build is reported as
// stages reports it, not as an error.
func TestLogQueuedBuildWithoutFollow(t *testing.T) {
	queueServer(t, 42, 1<<30)

	for _, args := range [][]string{
		{"log", "my-app", "42"},
		{"log", "my-app", "42", "--tail", "5"},
		{"log", "my-app", "42", "-f", "--grep", "x"},
	} {
		var out string
		var err error
		stderr := captureStderr(t, func() { out, err = executeCmd(t, args...) })
		require.NoError(t, err, args)
		assert.Empty(t, out, args)
		assert.Contains(t, stderr, "build #42 is queued", args)
	}
}

func TestStatusQueuedBuild(t *testing.T) {
	queueServer(t, 42, 1<<30)

	for _, extra := range [][]string{nil, {"--json"}, {"--format", "{{.Number}} {{.Queued}}"}} {
		var out string
		var err error
		stderr := captureStderr(t, func() { out, err = executeCmd(t, append([]string{"status", "my-app", "42"}, extra...)...) })
		require.NoError(t, err, extra)
		assert.Contains(t, stderr, "build #42 is queued", extra)
		if extra == nil {
			assert.Empty(t, out)
			continue
		}
		if extra[0] == "--format" {
			assert.Equal(t, "42 true", out)
			continue
		}
		assert.JSONEq(t, `{"number":42,"result":"","building":false,"duration":0,"timestamp":0,"url":"","queued":true}`, out)
	}
}

func TestStagesEmptyWhileRunning(t *testing.T) {
	emptyStagesServer(t, map[string]any{"number": 5, "building": true})

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "stages", "my-app", "5") })
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "no stages yet (build #5 is running)")
	assert.NotContains(t, stderr, "plugin")

	out, err = executeCmd(t, "stages", "my-app", "5", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, "[]", out)
}

// stageAppearsServer serves build #5, queued for the first queuedReads build
// reads and then running. Its stage list holds only "Build" for the first
// hiddenPolls stage reads, then "Big" (id 9) too. Big runs one step, 11,
// until its stage reads reach doneAt. finishAt > 0 ends the build on that
// build read.
func stageAppearsServer(t *testing.T, queuedReads, hiddenPolls, doneAt, finishAt int) {
	t.Helper()
	var mu sync.Mutex
	stageReads, buildReads := 0, 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		bigDone := stageReads >= doneAt
		queued := buildReads < queuedReads
		switch {
		case p == "/api/json":
			staplertest.WriteRoot(w, staplertest.Streaming)
		case p == "/queue/api/json":
			var items []map[string]any
			if queued {
				items = append(items, map[string]any{"id": 8, "task": map[string]any{"name": "my-app", "url": srv.URL + "/job/my-app/"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case p == "/job/my-app/api/json":
			next := 5
			if !queued {
				next++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"url": srv.URL + "/job/my-app/", "nextBuildNumber": next})
		case p == "/job/my-app/5/api/json":
			buildReads++
			if queued {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if finishAt > 0 && buildReads >= finishAt {
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": false, "result": "SUCCESS"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": true})
		case p == "/job/my-app/5/stages/tree":
			if queued {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			stageReads++
			stages := []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": "success"}}
			if stageReads > hiddenPolls {
				state := "running"
				if bigDone {
					state = "success"
				}
				stages = append(stages, map[string]any{"id": "9", "name": "Big", "type": "STAGE", "state": state})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"stages": stages}})
		case p == "/job/my-app/5/stages/steps":
			state := "running"
			if bigDone {
				state = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"steps": []map[string]any{
				{"id": "11", "name": "sh", "state": state, "stageId": "9"},
			}}})
		case p == "/job/my-app/5/execution/node/11/log/logText/progressiveText":
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "big output\n", !bigDone)
		case strings.HasPrefix(p, "/job/my-app/5/execution/node/"):
			_, _ = w.Write([]byte("<html>flow node</html>"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
}

func TestLogStageFollowWaitsForStageToStart(t *testing.T) {
	fastStagePolls(t)
	stageAppearsServer(t, 0, 3, 6, 0)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage", "Big", "-f") })
	require.NoError(t, err)
	assert.Equal(t, "big output\n", out)
	assert.Equal(t, 1, strings.Count(stderr, `note: stage "Big" has not started yet; waiting for it in build #5`), stderr)
}

func TestLogStageFollowBuildEndsWithoutStage(t *testing.T) {
	fastStagePolls(t)
	stageAppearsServer(t, 0, 1<<30, 1<<30, 3)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Big", "-f")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `stage "Big" never ran — build #5 finished SUCCESS`)
}

func TestLogStageWithoutFollowFailsAtOnce(t *testing.T) {
	stageAppearsServer(t, 0, 1<<30, 1<<30, 0)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Big")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `stage "Big" not found`)
}

// A queued poll costs three requests, so it runs at the wait interval, not
// the stage follow interval.
func TestLogStageFollowWaitsForQueuedBuild(t *testing.T) {
	fastStagePolls(t)
	old := waitPollInterval
	waitPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { waitPollInterval = old })
	stageAppearsServer(t, 2, 1, 4, 0)

	var out string
	var err error
	start := time.Now()
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage", "Big", "-f") })
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "one sleep after a queued poll")
	assert.Equal(t, "big output\n", out)
	assert.Equal(t, 1, strings.Count(stderr, "note: build #5 has not started yet; waiting for it to leave the queue"), stderr)
}

func TestLogStageFollowAmbiguousFailsAtOnce(t *testing.T) {
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

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Run Bazel Build", "-f")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "--stage-id")
}
