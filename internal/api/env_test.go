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
	assert.Equal(t, jenkins.EnvSourceInjected, env.Source)
	assert.Equal(t, "42", env.Vars["BUILD_NUMBER"])
	assert.Equal(t, "main", env.Vars["GIT_BRANCH"])
}

// A build that recorded nothing from either source must say so, naming both,
// rather than blaming a plugin.
func TestGetBuildEnvNeitherSourceRecordedAnything(t *testing.T) {
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

// A pipeline build never creates an EnvInject action, so the 404 there is the
// normal case and must not be reported as a missing plugin.
func TestGetBuildEnvFallsBackToThePipelineRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/injectedEnvVars/api/json") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"actions":[{},{"_class":"org.jenkinsci.plugins.workflow.cps.EnvActionImpl","environment":{"buildUrl":"https://ci/x","BUILD_REPO_URL":"https://git/x.git"}}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	env, err := client.GetBuildEnv("team/svc", 42)
	require.NoError(t, err)
	assert.Equal(t, jenkins.EnvSourcePipeline, env.Source)
	assert.Equal(t, "https://ci/x", env.Vars["buildUrl"])
	assert.NotContains(t, env.Vars, "BUILD_NUMBER", "EnvActionImpl carries only env.* assignments")
}

// A missing build must not be reported as a build that recorded no variables.
func TestGetBuildEnvMissingBuildIsNotAnEmptyEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetBuildEnv("team/svc", 99)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no build team/svc #99")
	assert.NotContains(t, err.Error(), "EnvInject")
}
