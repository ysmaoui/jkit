package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetJobParametersMixedTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/job/team/job/svc/api/json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"property": [
				{"_class": "com.example.SomeOtherProperty"},
				{
					"_class": "hudson.model.ParametersDefinitionProperty",
					"parameterDefinitions": [
						{"_class": "hudson.model.StringParameterDefinition", "name": "BRANCH", "type": "StringParameterDefinition", "description": "target branch", "defaultParameterValue": {"value": "main"}},
						{"_class": "hudson.model.ChoiceParameterDefinition", "name": "ENV", "type": "ChoiceParameterDefinition", "choices": ["dev", "staging", "prod"]},
						{"_class": "hudson.model.BooleanParameterDefinition", "name": "DRY_RUN", "type": "BooleanParameterDefinition", "defaultParameterValue": {"value": true}},
						{"_class": "hudson.model.PasswordParameterDefinition", "name": "TOKEN", "type": "PasswordParameterDefinition", "defaultParameterValue": {"value": "s3cr3t"}}
					]
				}
			]
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	params, err := client.GetJobParameters("team/svc")
	require.NoError(t, err)
	require.Len(t, params, 4)

	// string
	assert.Equal(t, "BRANCH", params[0].Name)
	assert.Equal(t, "string", params[0].Kind())
	assert.Equal(t, "main", params[0].DefaultString())
	assert.Equal(t, "target branch", params[0].Description)

	// choice
	assert.Equal(t, "choice", params[1].Kind())
	assert.Equal(t, []string{"dev", "staging", "prod"}, params[1].Choices())

	// boolean
	assert.Equal(t, "boolean", params[2].Kind())
	assert.Equal(t, "true", params[2].DefaultString())

	// password — default must be masked, never leaked
	assert.Equal(t, "password", params[3].Kind())
	assert.True(t, params[3].IsSecret())
	assert.NotContains(t, params[3].DefaultString(), "s3cr3t")
}

func TestGetJobParametersUnparameterized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"property": [{"_class": "com.example.GitLabConnectionProperty"}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	params, err := client.GetJobParameters("team/svc")
	require.NoError(t, err)
	assert.Empty(t, params)
}
