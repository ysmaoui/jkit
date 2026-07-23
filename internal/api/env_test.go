package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBuildEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/job/team/job/svc/42/injectedEnvVars/api/json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.envinject.EnvInjectVarList","envMap":{"BUILD_NUMBER":"42","GIT_BRANCH":"main"}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	env, err := client.GetBuildEnv("team/svc", 42)
	require.NoError(t, err)
	assert.Equal(t, "42", env["BUILD_NUMBER"])
	assert.Equal(t, "main", env["GIT_BRANCH"])
}

// When /injectedEnvVars 404s (plugin missing or build gone) and the job is a
// normal leaf job, the error must explain EnvInject, not surface a bare 404.
func TestGetBuildEnvPluginMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/injectedEnvVars/api/json") {
			http.NotFound(w, r)
			return
		}
		// job inspection: a plain leaf job, not a container
		_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetBuildEnv("team/svc", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EnvInject")
}
