package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../jenkins/testdata/" + name)
	require.NoError(t, err)
	return b
}

func TestGetJobConfigXML(t *testing.T) {
	body := fixture(t, "config_multibranch.xml")
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	got, err := client.GetJobConfigXML("team/widget")
	require.NoError(t, err)
	assert.Equal(t, "/job/team/job/widget/config.xml", gotPath)
	assert.Equal(t, body, got, "the raw document is returned untouched")
}

func TestGetJobDefinition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "config_multibranch.xml"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	def, err := client.GetJobDefinition("team/widget")
	require.NoError(t, err)
	assert.Equal(t, "team/widget", def.JobPath)
	assert.Equal(t, "multibranch pipeline", def.Kind)
	assert.Empty(t, def.Parent, "a multibranch container is not a branch child")
}

// A branch child records where its discovery rules actually live.
func TestGetJobDefinitionBranchChildKnowsItsParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "config_branch.xml"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	def, err := client.GetJobDefinition("team/widget/feature%2Fbuild-tweak")
	require.NoError(t, err)
	assert.Equal(t, "team/widget", def.Parent)
}

// Reading config.xml needs a permission plain job reads do not, so a 403 must
// name it rather than surface as a bare access-denied.
func TestGetJobConfigXMLPermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetJobConfigXML("team/svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/ExtendedRead")

	var pe *jenkins.PermissionError
	assert.ErrorAs(t, err, &pe, "the underlying cause must stay inspectable")
}

func TestGetJobConfigXMLNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.GetJobConfigXML("team/nope")
	require.Error(t, err)

	var nfe *jenkins.NotFoundError
	require.ErrorAs(t, err, &nfe)
	assert.Equal(t, "team/nope", nfe.Name)
}
