package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// When a build is requested on a multibranch container, GetBuild should return a
// ContainerBuildError listing the branches rather than a bare 404.
func TestGetBuildOnMultibranchContainer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/job/SANDBOX-VEMB/job/vemb/6/api/json":
			http.NotFound(w, r)
		case r.URL.Path == "/job/SANDBOX-VEMB/job/vemb/api/json":
			_, _ = w.Write([]byte(`{
				"_class": "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject",
				"name": "vemb",
				"jobs": [
					{"name": "main", "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob"},
					{"name": "feature/foo", "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetBuild("SANDBOX-VEMB/vemb", 6)
	require.Error(t, err)

	var cbe *jenkins.ContainerBuildError
	require.ErrorAs(t, err, &cbe)
	assert.Equal(t, "multibranch pipeline", cbe.Kind)
	assert.Equal(t, []string{"main", "feature/foo"}, cbe.Children)
	assert.Contains(t, cbe.Error(), "--branch")
	assert.Contains(t, cbe.Error(), "feature/foo")
}

// A 404 on a genuinely missing leaf job (not a container) must remain a plain
// NotFoundError, not be mistaken for a container.
func TestGetBuildOnMissingLeafJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/99/api/json"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/api/json"):
			_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"main"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetBuild("team/svc", 99)
	require.Error(t, err)

	var cbe *jenkins.ContainerBuildError
	assert.NotErrorAs(t, err, &cbe, "leaf job 404 must not become a ContainerBuildError")

	var nfe *jenkins.NotFoundError
	assert.ErrorAs(t, err, &nfe)
}
