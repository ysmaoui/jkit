package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestGetPGVTreeNested(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/job/team/job/svc/7/stages/tree" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","data":{"complete":false,"stages":[
			{"id":"2","type":"PIPELINE_START","state":"success","children":[]},
			{"id":"5","name":"Par","type":"STAGE","state":"running","children":[{"id":"6","name":"a","type":"PARALLEL","state":"running","children":[]}]}]}}`)
	}))
	defer srv.Close()

	stages, complete, err := NewClient(srv.URL, "u", "t").GetPGVTree(context.Background(), "team/svc", 7)
	require.NoError(t, err)
	assert.False(t, complete)
	require.Len(t, stages, 2, "start node is the caller's to drop")
	require.Len(t, stages[1].Children, 1)
	assert.Equal(t, "PARALLEL", stages[1].Children[0].Type)
}

func TestGetPGVTreeWithoutPGV(t *testing.T) {
	srv := allStepsMissingServer("org.jenkinsci.plugins.workflow.job.WorkflowRun", true)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").GetPGVTree(context.Background(), "team/svc", 7)
	var pme *jenkins.PluginMissingError
	require.ErrorAs(t, err, &pme)
	assert.Equal(t, "stages/tree", pme.Route)
}

func TestGetPGVTreeMissingBuild(t *testing.T) {
	srv := allStepsMissingServer("", true)
	defer srv.Close()

	_, _, err := NewClient(srv.URL, "u", "t").GetPGVTree(context.Background(), "team/svc", 7)
	var pme *jenkins.PluginMissingError
	require.False(t, errors.As(err, &pme))
	var nfe *jenkins.NotFoundError
	require.ErrorAs(t, err, &nfe)
	assert.Equal(t, "build", nfe.Resource)
}
