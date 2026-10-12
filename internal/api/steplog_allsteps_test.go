package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func allStepsServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/job/team/job/svc/7/stages/allSteps" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestListAllSteps(t *testing.T) {
	srv := allStepsServer(t, map[string]any{"status": "ok", "data": map[string]any{
		"runIsComplete": true,
		"steps": []map[string]any{
			{"id": "10", "name": "sh", "title": "Shell Script", "state": "success", "type": "STEP", "stageId": "5", "startTimeMillis": 100, "totalDurationMillis": 200, "pauseDurationMillis": 3},
			{"id": "20", "name": "echo", "state": "failure", "type": "STEP", "stageId": "6"},
		},
	}})
	defer srv.Close()

	steps, done, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	require.NoError(t, err)
	assert.True(t, done)
	require.Len(t, steps, 2)
	assert.Equal(t, "5", steps[0].StageID)
	assert.Equal(t, "6", steps[1].StageID)
	assert.Equal(t, "Shell Script", steps[0].Title)
	assert.Equal(t, int64(100), steps[0].StartTimeMillis)
	assert.Equal(t, int64(200), steps[0].TotalDurationMillis)
	assert.Equal(t, int64(3), steps[0].PauseDurationMillis)
}

func TestListAllStepsRunning(t *testing.T) {
	srv := allStepsServer(t, map[string]any{"status": "ok", "data": map[string]any{
		"runIsComplete": false,
		"steps":         []map[string]any{{"id": "10", "state": "running", "stageId": "5"}},
	}})
	defer srv.Close()

	steps, done, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, steps, 1)
	assert.Equal(t, "running", steps[0].State)
}

func allStepsMissingServer(class string, jobExists bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case class != "" && r.URL.Path == "/job/team/job/svc/7/api/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"_class": class})
		case jobExists && r.URL.Path == "/job/team/job/svc/api/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestListAllStepsWithoutPGV(t *testing.T) {
	srv := allStepsMissingServer("org.jenkinsci.plugins.workflow.job.WorkflowRun", true)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	var pme *jenkins.PluginMissingError
	require.ErrorAs(t, err, &pme)
	assert.Equal(t, "stages/allSteps", pme.Route)
	assert.Contains(t, err.Error(), "Pipeline Graph View")
	var nfe *jenkins.NotFoundError
	assert.ErrorAs(t, err, &nfe)
}

func TestListAllStepsMissingBuild(t *testing.T) {
	srv := allStepsMissingServer("", true)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	var pme *jenkins.PluginMissingError
	require.False(t, errors.As(err, &pme))
	var nfe *jenkins.NotFoundError
	require.ErrorAs(t, err, &nfe)
	assert.Equal(t, "build", nfe.Resource)
	assert.Contains(t, nfe.Name, "team/svc #7")
}

func TestListAllStepsMissingJob(t *testing.T) {
	srv := allStepsMissingServer("", false)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	var nfe *jenkins.NotFoundError
	require.ErrorAs(t, err, &nfe)
	assert.Equal(t, "job", nfe.Resource)
	assert.Equal(t, "team/svc", nfe.Name)
}

func TestListAllStepsNotPipelineBuild(t *testing.T) {
	srv := allStepsMissingServer("hudson.model.FreeStyleBuild", true)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").ListAllSteps(context.Background(), "team/svc", 7)
	var pme *jenkins.PluginMissingError
	require.False(t, errors.As(err, &pme))
	assert.Contains(t, err.Error(), "not a pipeline build")
}
