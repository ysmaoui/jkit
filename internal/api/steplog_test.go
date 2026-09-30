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
)

func TestLFWriterUndoesCRLFRewrite(t *testing.T) {
	tests := map[string]struct {
		writes []string
		want   string
	}{
		"rewritten LF":        {[]string{"a\r\nb\r\n"}, "a\nb\n"},
		"CR split from LF":    {[]string{"a\r", "\nb"}, "a\nb"},
		"lone CR kept":        {[]string{"50%\r60%\r\n"}, "50%\r60%\n"},
		"lone CR at boundary": {[]string{"50%\r", "60%"}, "50%\r60%"},
		"trailing CR flushed": {[]string{"a\r"}, "a\r"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			w := &lfWriter{w: &out}
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				require.NoError(t, err)
				assert.Equal(t, len(s), n)
			}
			require.NoError(t, w.Flush())
			assert.Equal(t, tt.want, out.String())
		})
	}
}

func TestCopyStepLogFromRequiresTextSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "not a progressiveText answer")
	}))
	defer srv.Close()

	var out strings.Builder
	next, _, err := NewClient(srv.URL, "u", "t").CopyStepLogFrom(context.Background(), "svc", 5, "7", 12, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "X-Text-Size")
	assert.Equal(t, int64(12), next)
	assert.Empty(t, out.String())
}

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

func TestCountsTextSize(t *testing.T) {
	for version, want := range map[string]bool{
		"2.479.3": true, "2.504.3": true, "2.508": true,
		"2.509": false, "2.516.3": false, "2.568.3": false,
		"": false, "garbage": false,
	} {
		assert.Equal(t, want, countsTextSize(version), version)
	}
}

func TestCopyStepLogFromStreamingWithoutMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
		_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\ncut off")
	}))
	defer srv.Close()

	var out strings.Builder
	next, _, err := NewClient(srv.URL, "u", "t").CopyStepLogFrom(context.Background(), "svc", 5, "7", 12, &out)
	require.Error(t, err)
	assert.Equal(t, int64(12), next)
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

// TestCopyStepLogFromRestartedLog covers Stapler answering from offset 0 when
// the stored log is shorter than start. A plain answer is caught before its
// body is written; a streaming one only after its text, which is already out.
func TestCopyStepLogFromRestartedLog(t *testing.T) {
	cases := map[string]struct {
		answer  func(w http.ResponseWriter)
		written string
	}{
		"streaming": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\nfrom the top\n"+
				"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"start\":0,\"end\":13}\r\n--b--")
		}, "from the top\n"},
		"plain": {func(w http.ResponseWriter) {
			w.Header().Set("X-Text-Size", "13")
			w.Header().Set("X-Jenkins", "2.479.3")
			w.Header().Set("X-More-Data", "true")
			_, _ = fmt.Fprint(w, "from the top\r\n")
		}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.answer(w) }))
			defer srv.Close()

			var out strings.Builder
			next, _, err := NewClient(srv.URL, "u", "t").CopyStepLogFrom(context.Background(), "svc", 5, "7", 40, &out)
			assert.ErrorIs(t, err, errStepLogRestarted)
			assert.Equal(t, int64(40), next)
			assert.Equal(t, tc.written, out.String())
		})
	}
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
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			if r.URL.Query().Get("start") != "0" {
				_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\n"+
					"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"start\":5,\"end\":5}\r\n--b--")
				return
			}
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\nline\n"+
				"\r\n--b\r\nContent-Disposition: form-data;name=meta\r\n\r\n{\"completed\":false,\"start\":0,\"end\":5}\r\n--b--")
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
