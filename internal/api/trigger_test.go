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

// fakeTriggerJenkins mimics how Jenkins core routes build triggers. A POST to
// /build on a job with a ParametersDefinitionProperty goes through
// ParametersDefinitionProperty._doBuild, which reads req.getSubmittedForm();
// with no "json" field Stapler answers 400 "Nothing is submitted".
// /buildWithParameters on a job without that property answers 400
// "<job> is not parameterized". /buildWithParameters with no fields applies each
// definition's default. Unknown fields are ignored.
func fakeTriggerJenkins(t *testing.T, defaults map[string]string, got *map[string]string, hits *[]string) *httptest.Server {
	t.Helper()
	parameterized := defaults != nil
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crumbIssuer/api/json":
			http.NotFound(w, r)
		case "/job/team/job/svc/api/json":
			if !parameterized {
				_, _ = w.Write([]byte(`{"property":[{"_class":"com.example.Other"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"property":[{"_class":"hudson.model.ParametersDefinitionProperty","parameterDefinitions":[{"name":"ENV","defaultParameterValue":{"value":"dev"}}]}]}`))
		case "/job/team/job/svc/build":
			*hits = append(*hits, r.URL.Path)
			if parameterized {
				_ = r.ParseForm()
				if r.PostForm.Get("json") == "" {
					http.Error(w, "Nothing is submitted", http.StatusBadRequest)
					return
				}
			}
			w.Header().Set("Location", "/queue/item/7/")
			w.WriteHeader(http.StatusCreated)
		case "/job/team/job/svc/buildWithParameters":
			*hits = append(*hits, r.URL.Path)
			if !parameterized {
				http.Error(w, "team/svc is not parameterized", http.StatusBadRequest)
				return
			}
			_ = r.ParseForm()
			values := map[string]string{}
			for k, v := range defaults {
				values[k] = v
				if sent := r.PostForm.Get(k); sent != "" {
					values[k] = sent
				}
			}
			*got = values
			w.Header().Set("Location", "/queue/item/8/")
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTriggerBuildParameterizedNoParamsUsesDefaults(t *testing.T) {
	var got map[string]string
	var hits []string
	srv := fakeTriggerJenkins(t, map[string]string{"ENV": "dev"}, &got, &hits)
	defer srv.Close()

	id, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/svc", nil)
	require.NoError(t, err)
	assert.Equal(t, 8, id)
	assert.Equal(t, []string{"/job/team/job/svc/buildWithParameters"}, hits)
	assert.Equal(t, map[string]string{"ENV": "dev"}, got)
}

func TestTriggerBuildUnparameterizedNoParamsUsesBuild(t *testing.T) {
	var got map[string]string
	var hits []string
	srv := fakeTriggerJenkins(t, nil, &got, &hits)
	defer srv.Close()

	id, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/svc", nil)
	require.NoError(t, err)
	assert.Equal(t, 7, id)
	assert.Equal(t, []string{"/job/team/job/svc/build"}, hits)
}

func TestTriggerBuildWithParamsOverridesDefaults(t *testing.T) {
	var got map[string]string
	var hits []string
	srv := fakeTriggerJenkins(t, map[string]string{"ENV": "dev"}, &got, &hits)
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/svc", map[string]string{"ENV": "prod"})
	require.NoError(t, err)
	assert.Equal(t, []string{"/job/team/job/svc/buildWithParameters"}, hits)
	assert.Equal(t, map[string]string{"ENV": "prod"}, got)
}

// rejectingJenkins answers every trigger with 400 and serves the given
// property JSON for the job's parameter lookup.
func rejectingJenkins(t *testing.T, property, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/job/team/job/svc/api/json":
			_, _ = w.Write([]byte(`{"property":` + property + `}`))
		case "/job/team/job/svc/build", "/job/team/job/svc/buildWithParameters":
			http.Error(w, body, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTriggerBuildParamsToUnparameterizedJob(t *testing.T) {
	srv := rejectingJenkins(t, `[]`, "team/svc is not parameterized")
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/svc", map[string]string{"X": "1"})
	require.Error(t, err)
	var srvErr *jenkins.ServerError
	require.ErrorAs(t, err, &srvErr)
	assert.Equal(t, http.StatusBadRequest, srvErr.StatusCode)
	assert.Contains(t, err.Error(), "takes no parameters; trigger it without them")
	assert.NotContains(t, err.Error(), "jkit params")
}

func TestTriggerBuildBadRequestOnParameterizedJob(t *testing.T) {
	srv := rejectingJenkins(t, `[{"parameterDefinitions":[{"name":"ENV"}]}]`, "bad value")
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/svc", map[string]string{"ENV": "x"})
	require.Error(t, err)
	var srvErr *jenkins.ServerError
	require.ErrorAs(t, err, &srvErr)
	assert.Contains(t, err.Error(), "jkit params team/svc")
	assert.NotContains(t, err.Error(), "takes no parameters")
}

func TestTriggerBuildUnknownJob(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	_, err := NewClient(srv.URL, "u", "p").TriggerBuild("team/nope", nil)
	var nf *jenkins.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.True(t, strings.HasPrefix(err.Error(), "triggering build: "), err.Error())
}
