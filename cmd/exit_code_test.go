package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// triggerServer serves job my-app: build #42 to rebuild from, a trigger that
// queues item 10, and build #7 that the item becomes once it has a result. An
// empty result keeps the item queued, and "running" keeps build #7 running and
// calls onRunning, if set, on each poll of it. A non-zero status answers every
// request.
func triggerServer(t *testing.T, status int, result string, onRunning func()) {
	t.Helper()
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		switch {
		case r.URL.Path == "/job/my-app/api/json":
			_, _ = w.Write([]byte(`{"property":[]}`))
		case r.URL.Path == "/job/my-app/build" && r.Method == http.MethodPost:
			w.Header().Set("Location", srvURL+"/queue/item/10/")
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/queue/item/10/api/json":
			item := map[string]any{"id": 10, "why": "waiting"}
			if result != "" {
				item = map[string]any{"id": 10, "executable": map[string]any{"number": 7, "url": srvURL + "/job/my-app/7/"}}
			}
			_ = json.NewEncoder(w).Encode(item)
		case r.URL.Path == "/job/my-app/42/api/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 42, "building": false, "result": "SUCCESS", "actions": []any{}})
		case r.URL.Path == "/job/my-app/7/api/json":
			if result == "running" {
				if onRunning != nil {
					onRunning()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "building": true})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "building": false, "result": result, "duration": 1000})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	setupTestConfig(t, srv.URL)
}

// interruptNow makes every wait start already interrupted, as after Ctrl-C.
func interruptNow(t *testing.T) {
	old := interruptContext
	interruptContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { interruptContext = old })
}

var waitingCommands = map[string][]string{
	"wait":          {"wait", "my-app", "42"},
	"run --wait":    {"run", "my-app", "--wait"},
	"rebuild --log": {"rebuild", "my-app", "42", "--log"},
}

func TestWaitingCommandsErrorExit6(t *testing.T) {
	for name, args := range waitingCommands {
		for status, msg := range map[int]string{http.StatusNotFound: "not found", http.StatusUnauthorized: "not authenticated"} {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				triggerServer(t, status, "", nil)
				_, err := executeCmd(t, args...)
				assert.Equal(t, 6, exitCode(t, err))
				assert.Contains(t, err.Error(), msg)
			})
		}
	}
}

func TestWaitingCommandsInterruptExit130(t *testing.T) {
	for name, args := range waitingCommands {
		t.Run(name, func(t *testing.T) {
			fastWaitPolls(t)
			waitServer(t, []map[string]any{running}, []string{""})
			if name != "wait" {
				triggerServer(t, 0, "", nil)
			}
			interruptNow(t)
			_, err := executeCmd(t, args...)
			assert.Equal(t, 130, exitCode(t, err))
			assert.Equal(t, "interrupted", err.Error())
		})
	}
}

func TestRunAndRebuildInterruptWhileBuildRunsExit130(t *testing.T) {
	oldPoll := buildPollInterval
	buildPollInterval = time.Hour
	t.Cleanup(func() { buildPollInterval = oldPoll })

	for _, args := range [][]string{{"run", "my-app", "--wait"}, {"rebuild", "my-app", "42", "--wait"}} {
		t.Run(args[0], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			old := interruptContext
			interruptContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
			t.Cleanup(func() { interruptContext = old })
			triggerServer(t, 0, "running", cancel)

			var err error
			stderr := captureStderr(t, func() { _, err = executeCmd(t, args...) })
			assert.Equal(t, 130, exitCode(t, err))
			assert.Contains(t, stderr, "Build #7 started")
		})
	}
}

// Ctrl-C ends --log streaming first; the finished build's result must not
// replace the interrupt.
func TestRunAndRebuildLogInterruptExit130(t *testing.T) {
	for _, args := range [][]string{{"run", "my-app", "--log"}, {"rebuild", "my-app", "42", "--log"}} {
		t.Run(args[0], func(t *testing.T) {
			triggerServer(t, 0, "SUCCESS", nil)
			interruptNow(t)
			_, err := executeCmd(t, args...)
			assert.Equal(t, 130, exitCode(t, err))
		})
	}
}

func TestRunAndRebuildWaitResultCodes(t *testing.T) {
	for _, args := range [][]string{{"run", "my-app", "--wait"}, {"rebuild", "my-app", "42", "--wait"}} {
		for result, want := range map[string]int{"SUCCESS": 0, "FAILURE": 1, "UNSTABLE": 2, "ABORTED": 3, "NOT_BUILT": 4} {
			t.Run(args[0]+"/"+result, func(t *testing.T) {
				triggerServer(t, 0, result, nil)
				_, err := executeCmd(t, args...)
				assert.Equal(t, want, exitCode(t, err))
			})
		}
	}
}

func TestRunAndRebuildWaitTimeoutsExit5(t *testing.T) {
	oldQueue, oldBuild, oldPoll := queueTimeout, buildTimeout, buildPollInterval
	queueTimeout, buildTimeout, buildPollInterval = time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() { queueTimeout, buildTimeout, buildPollInterval = oldQueue, oldBuild, oldPoll })

	for _, args := range [][]string{{"run", "my-app", "--wait"}, {"rebuild", "my-app", "42", "--wait"}} {
		for result, msg := range map[string]string{"": "queue timeout after 1ms", "running": "build timeout after 1ms"} {
			t.Run(args[0]+"/"+msg, func(t *testing.T) {
				triggerServer(t, 0, result, nil)
				_, err := executeCmd(t, args...)
				assert.Equal(t, 5, exitCode(t, err))
				assert.Contains(t, err.Error(), msg)
			})
		}
	}
}

func TestWaitUsageErrorExit6(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":   {"wait", "my-app", "--bogus"},
		"too many args":  {"wait", "my-app", "42", "43"},
		"bad flag value": {"wait", "my-app", "--max-wait", "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			triggerServer(t, 0, "", nil)
			_, err := executeCmd(t, args...)
			assert.Equal(t, 6, exitCode(t, err))
		})
	}
}

func TestRunWaitTooManyArgsExit6(t *testing.T) {
	triggerServer(t, 0, "", nil)
	_, err := executeCmd(t, "run", "my-app", "other", "--wait")
	assert.Equal(t, 6, exitCode(t, err))
}

// Without --wait the exit code carries no result, so errors keep exit 1.
func TestRunAndRebuildWithoutWaitErrorsAreNotExitErrors(t *testing.T) {
	for _, args := range [][]string{{"run", "my-app"}, {"rebuild", "my-app", "42"}, {"run", "my-app", "--bogus", "--wait"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			triggerServer(t, http.StatusNotFound, "", nil)
			_, err := executeCmd(t, args...)
			require.Error(t, err)
			var ee *jenkins.ExitError
			if errors.As(err, &ee) {
				t.Errorf("want exit 1, got %d", ee.Code)
			}
		})
	}
}
