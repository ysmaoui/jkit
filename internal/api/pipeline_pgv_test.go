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

func pgvTreeHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stages/tree") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{
					"complete": true,
					"stages": []map[string]any{
						{"id": "1", "name": "Build", "state": "success", "type": "STAGE", "totalDurationMillis": 5000},
						{"id": "2", "name": "Test", "state": "failure", "type": "STAGE", "totalDurationMillis": 3000},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGetPipelineStagesPrefersPGV(t *testing.T) {
	var pgvCalls, blueCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stages/tree") {
			atomic.AddInt32(&pgvCalls, 1)
			pgvTreeHandler(t)(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/blue/rest/") {
			atomic.AddInt32(&blueCalls, 1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t")
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, stages, 2)
	assert.Equal(t, "Build", stages[0].Name)
	assert.Equal(t, "SUCCESS", stages[0].Status)
	assert.Equal(t, "FAILURE", stages[1].Status)
	assert.Equal(t, int32(1), atomic.LoadInt32(&pgvCalls))
	assert.Equal(t, int32(0), atomic.LoadInt32(&blueCalls), "blue ocean must not be called when PGV responds")
}

func TestGetPipelineStagesFallsBackToBlueOceanOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/nodes/") {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "10", "displayName": "LegacyStage", "result": "SUCCESS", "durationInMillis": 1000},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound) // PGV path returns 404
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t")
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, stages, 1)
	assert.Equal(t, "LegacyStage", stages[0].Name)
}

func TestGetPipelineStagesForcedBlueOceanSkipsPGV(t *testing.T) {
	var pgvCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stages/tree") {
			atomic.AddInt32(&pgvCalls, 1)
			pgvTreeHandler(t)(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/nodes/") {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "10", "displayName": "BlueStage", "result": "SUCCESS"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t", WithPipelineSource(PipelineSourceBlueOcean))
	stages, err := client.GetPipelineStages("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, stages, 1)
	assert.Equal(t, "BlueStage", stages[0].Name)
	assert.Equal(t, int32(0), atomic.LoadInt32(&pgvCalls), "PGV must not be called when forced blueocean")
}

func TestGetPipelineStagesForcedPGVErrorsWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t", WithPipelineSource(PipelineSourcePGV))
	_, err := client.GetPipelineStages("team/svc", 42)
	require.Error(t, err)
}

func TestGetStageLogPrefersPGV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stages/log") && r.URL.Query().Get("nodeId") == "42" {
			fmt.Fprint(w, "pgv-log-for-42")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t")
	log, err := client.GetStageLog("team/svc", 7, "42")
	require.NoError(t, err)
	assert.Equal(t, "pgv-log-for-42", log)
}

func TestGetStageLogFallsBackToBlueOceanOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/nodes/42/log/") {
			fmt.Fprint(w, "blue-ocean-log")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "u", "t")
	log, err := client.GetStageLog("team/svc", 7, "42")
	require.NoError(t, err)
	assert.Equal(t, "blue-ocean-log", log)
}

func TestParsePipelineSource(t *testing.T) {
	cases := map[string]PipelineSource{
		"":                    PipelineSourceAuto,
		"auto":                PipelineSourceAuto,
		"pgv":                 PipelineSourcePGV,
		"PGV":                 PipelineSourcePGV,
		"pipeline-graph-view": PipelineSourcePGV,
		"blueocean":           PipelineSourceBlueOcean,
		"blue-ocean":          PipelineSourceBlueOcean,
		"blue":                PipelineSourceBlueOcean,
		"garbage":             PipelineSourceAuto,
	}
	for in, want := range cases {
		if got := parsePipelineSource(in); got != want {
			t.Errorf("parsePipelineSource(%q)=%v want %v", in, got, want)
		}
	}
}
