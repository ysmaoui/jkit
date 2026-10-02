package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/staplertest"
)

func TestListStageStepsFallsBackToBlueOcean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runs/5/nodes/4/steps/") && r.URL.Query().Get("start") == "0" {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "7", "state": "FINISHED", "result": "FAILURE"},
				{"id": "8", "state": "RUNNING", "result": "UNKNOWN"},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/nodes/4/steps/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	steps, fromPGV, err := NewClient(srv.URL, "u", "t").ListStageSteps(context.Background(), "team/svc", 5, "4")
	require.NoError(t, err)
	assert.False(t, fromPGV)
	// Blue Ocean's listing carries no exception text route, so no step is
	// marked for one.
	assert.Equal(t, []StageStep{{ID: "7"}, {ID: "8", Active: true}}, steps)
}

func TestListStageStepsPGV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/job/svc/5/stages/steps") && r.URL.Query().Get("nodeId") == "4" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"runIsComplete": false,
				"steps": []map[string]any{
					{"id": "6", "state": "success"},
					{"id": "7", "state": "failure"},
					{"id": "8", "state": "aborted"},
					{"id": "9", "state": "paused"},
					{"id": "10", "state": "running"},
				},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	steps, fromPGV, err := NewClient(srv.URL, "u", "t").ListStageSteps(context.Background(), "svc", 5, "4")
	require.NoError(t, err)
	assert.True(t, fromPGV)
	assert.Equal(t, []StageStep{
		{ID: "6"}, {ID: "7", Failed: true}, {ID: "8", Failed: true},
		{ID: "9", Active: true}, {ID: "10", Active: true},
	}, steps)
}

func TestListStageStepsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").ListStageSteps(context.Background(), "svc", 5, "4")
	assert.ErrorIs(t, err, ErrStepLogsUnavailable)
}

// TestStageStepFollowerKeepsCauseAfterOutput checks that once output is
// printed, a lost step listing reports its cause rather than asking for the
// whole-stage fallback, which would print it again.
func TestStageStepFollowerKeepsCauseAfterOutput(t *testing.T) {
	var gone atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case gone.Load():
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/stages/steps"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"steps": []map[string]any{{"id": "7", "state": "running"}},
			}})
		case strings.HasSuffix(r.URL.Path, "/log/logText/progressiveText"):
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\nline\n"+
				"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"end\":5}\r\n--b--")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := NewClient(srv.URL, "u", "t").NewStageStepFollower("svc", 5, "4")
	var out strings.Builder
	require.NoError(t, f.Poll(context.Background(), &out, false))
	assert.Equal(t, "line\n", out.String())

	gone.Store(true)
	err := f.Poll(context.Background(), &out, false)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStepLogsUnavailable)
	var nfe *jenkins.NotFoundError
	assert.ErrorAs(t, err, &nfe)
}

// stepListingServer lists one running step 7 whose streaming log is "line\n",
// failing the listing with 500 on the calls fail marks.
func stepListingServer(t *testing.T, fail func(call int) bool) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/steps"):
			if fail(int(calls.Add(1))) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"steps": []map[string]any{{"id": "7", "state": "running"}},
			}})
		case strings.HasSuffix(r.URL.Path, "/log/logText/progressiveText"):
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "line\n", true)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStageStepFollowerGivesUpOnListingAfterOutput(t *testing.T) {
	srv := stepListingServer(t, func(call int) bool { return call > 1 })
	f := NewClient(srv.URL, "u", "t").NewStageStepFollower("svc", 5, "4")
	var out strings.Builder
	ctx := context.Background()
	require.NoError(t, f.Poll(ctx, &out, false))
	assert.Equal(t, "line\n", out.String())

	for i := 1; i < maxStepListFailures; i++ {
		require.NoError(t, f.Poll(ctx, &out, false), "failure %d is retried on the next poll", i)
	}
	err := f.Poll(ctx, &out, false)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStepLogsUnavailable)
	var se *jenkins.ServerError
	assert.ErrorAs(t, err, &se)
}

func TestStageStepFollowerFinalReadRetriesListingAtOnce(t *testing.T) {
	srv := stepListingServer(t, func(call int) bool { return call == 1 })
	f := NewClient(srv.URL, "u", "t").NewStageStepFollower("svc", 5, "4")
	var out strings.Builder
	require.NoError(t, f.Poll(context.Background(), &out, true))
	assert.Equal(t, "line\n", out.String())
}

// TestStageStepFollowerKeepsStepErrorAfterOutput checks a step log failure
// after output keeps its type instead of asking for the fallback.
func TestStageStepFollowerKeepsStepErrorAfterOutput(t *testing.T) {
	var forbid atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/steps"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"steps": []map[string]any{{"id": "7", "state": "running"}},
			}})
		case forbid.Load():
			w.WriteHeader(http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, "/log/logText/progressiveText"):
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\nline\n"+
				"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"start\":0,\"end\":5}\r\n--b--")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := NewClient(srv.URL, "u", "t").NewStageStepFollower("svc", 5, "4")
	var out strings.Builder
	require.NoError(t, f.Poll(context.Background(), &out, false))
	forbid.Store(true)
	err := f.Poll(context.Background(), &out, false)
	assert.NotErrorIs(t, err, ErrStepLogsUnavailable)
	var pe *jenkins.PermissionError
	assert.ErrorAs(t, err, &pe)
}
