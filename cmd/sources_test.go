package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourcesTestServer answers the job lookup and the build's action tree, so the
// command can be driven with and without an explicit build number.
func sourcesTestServer(t *testing.T, actions []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/job/my-app/api/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "my-app", "lastBuild": map[string]any{"number": 24},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_class":  "org.jenkinsci.plugins.workflow.job.WorkflowRun",
			"actions": actions,
		})
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
	return srv
}

func gitBuildData(remote, sha, branch string) map[string]any {
	return map[string]any{
		"_class":            "hudson.plugins.git.util.BuildData",
		"remoteUrls":        []string{remote},
		"lastBuiltRevision": map[string]any{"SHA1": sha, "branch": []map[string]any{{"name": branch}}},
	}
}

func libraries(libs ...map[string]any) map[string]any {
	return map[string]any{
		"_class":    "org.jenkinsci.plugins.workflow.libs.LibrariesAction",
		"libraries": libs,
	}
}

// liveShapedActions mirrors what build 24 of the reference job returns: a
// branch-source revision, three shared libraries on moving branches, and a
// BuildData for each of their repositories.
func liveShapedActions() []map[string]any {
	return []map[string]any{
		{"_class": "hudson.model.CauseAction"},
		{},
		{"_class": "jenkins.scm.api.SCMRevisionAction", "revision": map[string]any{
			"_class": "jenkins.plugins.git.AbstractGitSCMSource$SCMRevisionImpl",
			"hash":   "65e714a68fcf8ec58ee7e162c7ae8348696b7a4f",
		}},
		gitBuildData("https://git.example.com/CARIAD/tools-sdk-global-jenkins-shared-lib.git",
			"e3fc9e5c9c9ab32e6318ea088d67c6c2b29b7aa0", "develop"),
		gitBuildData("https://git.example.com/CARIAD/tools-hera-2.git",
			"52d790ee106971aca82863770dbb464f503de21a", "feature/bazel-remote-exec"),
		libraries(
			map[string]any{"name": "e3-sdk-global-jenkins-shared-lib", "version": "develop", "trusted": true},
			map[string]any{"name": "hera2", "version": "feature/bazel-remote-exec", "trusted": false},
			map[string]any{"name": "premium", "version": "feature/prepare-for-bazel", "trusted": true},
		),
	}
}

func TestSourcesCommand(t *testing.T) {
	sourcesTestServer(t, liveShapedActions())

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)

	assert.Contains(t, out, "65e714a68fcf8ec58ee7e162c7ae8348696b7a4f")
	assert.Contains(t, out, "e3-sdk-global-jenkins-shared-lib @ develop  (trusted)")
	assert.Contains(t, out, "commit  e3fc9e5c9c9ab32e6318ea088d67c6c2b29b7aa0  (matched to a checkout by branch name)")
	assert.Contains(t, out, "hera2 @ feature/bazel-remote-exec  (untrusted)")
	assert.Contains(t, out, "-> shared library hera2")
}

// The library with no checkout to point at prints the gap and the reason for
// it, never a commit borrowed from one of the other two repositories.
func TestSourcesCommandReportsUnresolvableLibrary(t *testing.T) {
	sourcesTestServer(t, liveShapedActions())

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)

	assert.Contains(t, out, "premium @ feature/prepare-for-bazel")
	assert.Contains(t, out, `SHA not resolvable: no git checkout recorded the branch "feature/prepare-for-bazel"`)

	premium, _, _ := strings.Cut(out[strings.Index(out, "premium @"):], "\n\n")
	assert.NotContains(t, premium, "e3fc9e5c", "an unmatched library must not borrow another repository's commit")
	assert.NotContains(t, premium, "52d790ee")
}

func TestSourcesCommandNotesMovingLibraries(t *testing.T) {
	sourcesTestServer(t, liveShapedActions())

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)

	assert.Contains(t, out, "3 of 3 libraries are requested by name rather than by commit")
	assert.Contains(t, out, "1 of 3 libraries have no commit above")
}

// A library asked for by commit id needs no join, and must not be reported as
// though a checkout record had confirmed it.
func TestSourcesCommandLabelsPinnedLibrary(t *testing.T) {
	const sha = "e3fc9e5c9c9ab32e6318ea088d67c6c2b29b7aa0"
	sourcesTestServer(t, []map[string]any{
		libraries(map[string]any{"name": "hera2", "version": sha, "trusted": false}),
	})

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)

	assert.Contains(t, out, "commit  "+sha+"  (the requested version is itself a commit id)")
	assert.NotContains(t, out, "requested by name rather than by commit")
}

func TestSourcesCommandDefaultsToLatestBuild(t *testing.T) {
	sourcesTestServer(t, liveShapedActions())

	out, err := executeCmd(t, "sources", "my-app")
	require.NoError(t, err)
	assert.Contains(t, out, "Build:        #24")
}

// A build with no shared libraries must not read as a build whose libraries
// could not be determined.
func TestSourcesCommandBuildWithoutLibraries(t *testing.T) {
	sourcesTestServer(t, []map[string]any{
		{"_class": "hudson.model.CauseAction"},
		gitBuildData("https://git.example.com/team/the-app.git", "aaaabbbbccccdddd", "main"),
	})

	out, err := executeCmd(t, "sources", "my-app", "7")
	require.NoError(t, err)

	assert.Contains(t, out, "the build carries no LibrariesAction, so it loaded no shared library")
	assert.Contains(t, out, "https://git.example.com/team/the-app.git")
	assert.NotContains(t, out, "SHA not resolvable")
}

func TestSourcesCommandBuildWithoutCheckouts(t *testing.T) {
	sourcesTestServer(t, []map[string]any{{"_class": "hudson.model.CauseAction"}})

	out, err := executeCmd(t, "sources", "my-app", "7")
	require.NoError(t, err)

	assert.Contains(t, out, "the build carries no BuildData")
	assert.Contains(t, out, "not created from a branch source")
}

func TestSourcesCommandRedactsRemoteCredentials(t *testing.T) {
	actions := []map[string]any{
		libraries(map[string]any{"name": "hera2", "version": "develop", "trusted": false}),
		gitBuildData("https://bot:s3cr3t@git.example.com/CARIAD/tools-hera-2.git", "52d790ee", "develop"),
	}
	sourcesTestServer(t, actions)

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)
	assert.NotContains(t, out, "s3cr3t")
	assert.Contains(t, out, "git.example.com/CARIAD/tools-hera-2.git")
}

func TestSourcesCommandShowSecrets(t *testing.T) {
	actions := []map[string]any{
		libraries(map[string]any{"name": "hera2", "version": "develop", "trusted": false}),
		gitBuildData("https://bot:s3cr3t@git.example.com/CARIAD/tools-hera-2.git", "52d790ee", "develop"),
	}
	sourcesTestServer(t, actions)

	out, err := executeCmd(t, "sources", "my-app", "24", "--show-secrets")
	require.NoError(t, err)
	assert.Contains(t, out, "bot:s3cr3t@git.example.com")
}

func TestSourcesCommandJSON(t *testing.T) {
	sourcesTestServer(t, liveShapedActions())

	out, err := executeCmd(t, "sources", "my-app", "24", "--json")
	require.NoError(t, err)

	var got struct {
		Libraries []struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			SHA1       string `json:"sha1"`
			Resolution string `json:"resolution"`
			Reason     string `json:"reason"`
		} `json:"libraries"`
		PipelineRevisions []struct {
			Hash string `json:"hash"`
		} `json:"pipelineRevisions"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Libraries, 3)
	assert.Equal(t, "matched-by-branch", got.Libraries[0].Resolution)
	assert.Equal(t, "unresolved", got.Libraries[2].Resolution)
	assert.Empty(t, got.Libraries[2].SHA1)
	assert.NotEmpty(t, got.Libraries[2].Reason)
	assert.Equal(t, "65e714a68fcf8ec58ee7e162c7ae8348696b7a4f", got.PipelineRevisions[0].Hash)
}
