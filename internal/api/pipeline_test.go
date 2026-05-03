package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	log, err := client.GetStageLog("team/svc", 42, "10")
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
	_, err := client.GetStageLog("team/svc", 42, "10")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blue ocean plugin required")
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
	log, err := client.GetStageLog("svc", 42, "10")
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
	_, err := client.GetStageLog("svc", 42, "10")
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
	_, err := client.GetStageLog("svc", 42, "10")
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
	log, err := client.GetStageLog("svc", 42, "10")
	require.NoError(t, err)
	assert.Contains(t, log, "only good output")
}
