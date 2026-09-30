package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWarnIfNoAgentsStaysQuietWhenAnyStageReportsOne(t *testing.T) {
	var buf bytes.Buffer
	warnIfNoAgents(&buf, []stageInfo{{ID: "1"}, {ID: "2", Agent: "pod-abc"}})
	assert.Empty(t, buf.String())
}

func TestWarnIfNoAgentsNamesBothCauses(t *testing.T) {
	var buf bytes.Buffer
	warnIfNoAgents(&buf, []stageInfo{{ID: "1"}, {ID: "2"}})
	out := buf.String()
	assert.Contains(t, out, "no node")
	assert.Contains(t, out, "Blue Ocean")
}

const elapsedNowMillis = 1_700_000_000_000

func fixClock(t *testing.T) {
	t.Helper()
	orig := clock
	clock = func() time.Time { return time.UnixMilli(elapsedNowMillis) }
	t.Cleanup(func() { clock = orig })
}

// runningBuildServer serves build 5 as running for 1h2m with a finished
// "Build" stage and a "Test" stage running for 2m5s.
func runningBuildServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/tree"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{"complete": false, "stages": []map[string]any{
					{"id": "1", "name": "Build", "type": "STAGE", "state": "success", "totalDurationMillis": 5000, "startTimeMillis": elapsedNowMillis - 300_000},
					{"id": "2", "name": "Test", "type": "STAGE", "state": "running", "totalDurationMillis": 0, "startTimeMillis": elapsedNowMillis - 125_000},
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/5/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5, "building": true, "duration": 0,
				"timestamp": elapsedNowMillis - 3_725_000, "url": "http://jenkins/job/my-app/5/",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestStagesShowsElapsedForRunningStage(t *testing.T) {
	fixClock(t)
	srv := runningBuildServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "2m5s")
	assert.NotContains(t, out, "< 1s")
}

func TestStagesShowsElapsedForPausedStage(t *testing.T) {
	fixClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stages/tree") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"data": map[string]any{"complete": false, "stages": []map[string]any{
				{"id": "2", "name": "Approve", "type": "STAGE", "state": "paused", "startTimeMillis": elapsedNowMillis - 125_000},
			}},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "PAUSED_PENDING_INPUT")
	assert.Contains(t, out, "2m5s")
	assert.NotContains(t, out, "< 1s")
}

// Blue Ocean measures a running node's durationInMillis up to the request, so
// the fallback source needs no start time to show elapsed time.
func TestStagesShowsElapsedForBlueOceanRunningStage(t *testing.T) {
	fixClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/runs/5/nodes/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "7", "displayName": "Test", "type": "STAGE", "state": "RUNNING", "result": "UNKNOWN",
				"durationInMillis": 125_000, "startTime": "2023-11-14T22:11:15.000+0000"},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "2m5s")
	assert.NotContains(t, out, "< 1s")
}

func TestStagesJSONKeepsRawDuration(t *testing.T) {
	fixClock(t)
	srv := runningBuildServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"durationMillis": 0`)
}

func TestStatusDetailShowsElapsedForRunningBuildAndStage(t *testing.T) {
	fixClock(t)
	srv := runningBuildServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "status", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "Duration: 1h2m")
	assert.Contains(t, out, "2m5s")
	assert.NotContains(t, out, "< 1s")
}

func TestStatusListShowsElapsedForRunningBuild(t *testing.T) {
	fixClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"builds": []map[string]any{
				{"number": 5, "building": true, "duration": 0, "timestamp": elapsedNowMillis - 90_000},
			},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "status", "my-app")
	require.NoError(t, err)
	assert.Contains(t, out, "1m30s")
}

// stageEndpoints404Server 404s both stage endpoints. The build itself exists
// only when buildExists is true. The counter tracks build lookups.
func stageEndpoints404Server(t *testing.T, buildExists bool) (*httptest.Server, *int) {
	t.Helper()
	var buildRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/5/api/json") {
			buildRequests++
			if buildExists {
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "duration": 1, "url": "http://jenkins/job/my-app/5/"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return srv, &buildRequests
}

func TestStagesReportsMissingBuildNotPluginHint(t *testing.T) {
	srv, _ := stageEndpoints404Server(t, false)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "stages", "my-app", "5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Contains(t, err.Error(), "jkit list")
	assert.NotContains(t, err.Error(), "plugin required")
}

func TestStagesKeepsPluginHintWhenBuildExists(t *testing.T) {
	srv, buildRequests := stageEndpoints404Server(t, true)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "stages", "my-app", "5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin required")
	assert.Equal(t, 1, *buildRequests)
}

func TestLogStageReportsMissingBuildNotPluginHint(t *testing.T) {
	srv, _ := stageEndpoints404Server(t, false)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Build")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "plugin required")
}

func TestLogStageKeepsPluginHintWhenBuildExists(t *testing.T) {
	srv, _ := stageEndpoints404Server(t, true)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Build")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin required")
}

func TestLogStageIDReportsMissingBuildNotPluginHint(t *testing.T) {
	srv, _ := stageEndpoints404Server(t, false)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	for _, extra := range [][]string{nil, {"--tail", "5"}, {"-f"}} {
		args := append([]string{"log", "my-app", "5", "--stage-id", "4"}, extra...)
		_, err := executeCmd(t, args...)
		require.Error(t, err, extra)
		assert.Contains(t, err.Error(), "not found", extra)
		assert.NotContains(t, err.Error(), "plugin required", extra)
	}
}

func TestLogStageIDKeepsPluginHintWhenBuildExists(t *testing.T) {
	srv, _ := stageEndpoints404Server(t, true)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin required")
}
