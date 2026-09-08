package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const (
	buildDataClass   = "hudson.plugins.git.util.BuildData"
	librariesClass   = "org.jenkinsci.plugins.workflow.libs.LibrariesAction"
	scmRevisionClass = "jenkins.scm.api.SCMRevisionAction"
)

// sourcesRun wraps actions the way a WorkflowRun answers the tree query,
// including the unexported actions that render as {}.
func sourcesRun(actions ...map[string]any) map[string]any {
	all := []map[string]any{{"_class": "hudson.model.ParametersAction"}, {}}
	all = append(all, actions...)
	return map[string]any{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun", "actions": all}
}

func buildDataAction(remote, sha, branch string) map[string]any {
	return map[string]any{
		"_class":            buildDataClass,
		"remoteUrls":        []string{remote},
		"lastBuiltRevision": map[string]any{"SHA1": sha, "branch": []map[string]any{{"name": branch}}},
	}
}

func librariesAction(libs ...map[string]any) map[string]any {
	return map[string]any{"_class": librariesClass, "libraries": libs}
}

func library(name, version string, trusted bool) map[string]any {
	return map[string]any{"name": name, "version": version, "trusted": trusted}
}

func revisionAction(class, hash string) map[string]any {
	rev := map[string]any{"_class": class}
	if hash != "" {
		rev["hash"] = hash
	}
	return map[string]any{"_class": scmRevisionClass, "revision": rev}
}

type sourcesServer struct {
	*httptest.Server
	query url.Values
}

func newSourcesServer(t *testing.T, body any) *sourcesServer {
	t.Helper()
	s := &sourcesServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.query = r.URL.Query()
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func fetchSources(t *testing.T, body any) (*jenkins.BuildSources, *sourcesServer) {
	t.Helper()
	srv := newSourcesServer(t, body)
	src, err := NewClient(srv.URL, "u", "p").GetBuildSources("my-app", 24)
	require.NoError(t, err)
	return src, srv
}

func TestGetBuildSourcesReadsLibrariesCheckoutsAndRevision(t *testing.T) {
	src, _ := fetchSources(t, sourcesRun(
		revisionAction("jenkins.plugins.git.AbstractGitSCMSource$SCMRevisionImpl", "65e714a6"),
		buildDataAction("https://git/sdk.git", "e3fc9e5c", "develop"),
		librariesAction(library("e3-sdk", "develop", true), library("hera2", "feature/bazel", false)),
		buildDataAction("https://git/hera-2.git", "52d790ee", "feature/bazel"),
	))

	require.Len(t, src.PipelineRevisions, 1)
	assert.Equal(t, "65e714a6", src.PipelineRevisions[0].Hash)

	require.Len(t, src.Libraries, 2)
	assert.Equal(t, "e3fc9e5c", src.Libraries[0].SHA1)
	assert.True(t, src.Libraries[0].Trusted)
	assert.Equal(t, "52d790ee", src.Libraries[1].SHA1)
	assert.False(t, src.Libraries[1].Trusted)

	require.Len(t, src.Checkouts, 2)
	assert.Equal(t, "e3-sdk", src.Checkouts[0].Library)
	assert.Empty(t, src.Warnings)
}

// BuildData.buildsByBranchName keeps one entry per branch the JOB has ever
// built, so a run carries revisions recorded by earlier build numbers. Reading
// it would report code this build never ran.
func TestGetBuildSourcesIgnoresStaleBuildsByBranchName(t *testing.T) {
	const staleSHA = "78f9f4296f06bb5758269c4960967bcc4da1c3c2"
	action := buildDataAction("https://git/premium.git", "37cecaf2", "feature/prepare-for-bazel")
	action["buildsByBranchName"] = map[string]any{
		"feature/prepare-for-bazel": map[string]any{
			"buildNumber": 24,
			"revision":    map[string]any{"SHA1": "37cecaf2"},
			"marked":      map[string]any{"SHA1": "37cecaf2", "branch": []map[string]any{{"name": "feature/prepare-for-bazel"}}},
		},
		"feature/support-github-app-credentials": map[string]any{
			"buildNumber": 5,
			"revision":    map[string]any{"SHA1": staleSHA},
			"marked":      map[string]any{"SHA1": staleSHA, "branch": []map[string]any{{"name": "feature/support-github-app-credentials"}}},
		},
	}

	src, srv := fetchSources(t, sourcesRun(
		librariesAction(library("premium", "feature/prepare-for-bazel", true)),
		action,
	))

	require.Len(t, src.Checkouts, 1, "one BuildData is one checkout, however many branches it accumulated")
	assert.Equal(t, []string{"feature/prepare-for-bazel"}, src.Checkouts[0].Branches)
	assert.Equal(t, "37cecaf2", src.Checkouts[0].SHA1)
	assert.Equal(t, "37cecaf2", src.Libraries[0].SHA1)

	body, _ := json.Marshal(src)
	assert.NotContains(t, string(body), staleSHA, "a revision from build 5 must not appear in build 24's report")
	assert.NotContains(t, string(body), "feature/support-github-app-credentials")
	assert.NotContains(t, srv.query.Get("tree"), "buildsByBranchName", "the stale field must not even be requested")
}

// An absent LibrariesAction and one listing nothing are different states, and
// only the report can tell them apart for the caller.
func TestGetBuildSourcesDistinguishesAbsentLibrariesAction(t *testing.T) {
	absent, _ := fetchSources(t, sourcesRun(buildDataAction("https://git/app.git", "aaaa", "main")))
	assert.False(t, absent.LibrariesActionPresent)
	assert.Empty(t, absent.Libraries)

	empty, _ := fetchSources(t, sourcesRun(map[string]any{"_class": librariesClass}))
	assert.True(t, empty.LibrariesActionPresent)
	assert.Empty(t, empty.Libraries)
}

// Data read from a class that does not normally export it is kept, because
// dropping it would silently shorten the report, and flagged, because trusting
// it silently would hide the assumption.
func TestGetBuildSourcesWarnsOnUnexpectedActionClass(t *testing.T) {
	src, _ := fetchSources(t, sourcesRun(map[string]any{
		"_class":            "com.example.ExoticCheckoutAction",
		"remoteUrls":        []string{"https://git/app.git"},
		"lastBuiltRevision": map[string]any{"SHA1": "aaaa", "branch": []map[string]any{{"name": "main"}}},
	}))

	require.Len(t, src.Checkouts, 1)
	assert.Equal(t, "aaaa", src.Checkouts[0].SHA1)
	require.Len(t, src.Warnings, 1)
	assert.Contains(t, src.Warnings[0], "com.example.ExoticCheckoutAction")
	assert.Contains(t, src.Warnings[0], "BuildData")
}

// A pull-request or non-git revision exports its own fields instead of hash.
// The revision is still reported, with the class that withheld the commit.
func TestGetBuildSourcesKeepsRevisionWithoutHash(t *testing.T) {
	src, _ := fetchSources(t, sourcesRun(
		revisionAction("jenkins.scm.api.mixin.ChangeRequestSCMRevision", ""),
	))

	require.Len(t, src.PipelineRevisions, 1)
	assert.Empty(t, src.PipelineRevisions[0].Hash)
	assert.Equal(t, "jenkins.scm.api.mixin.ChangeRequestSCMRevision", src.PipelineRevisions[0].RevisionClass)
}

func TestGetBuildSourcesWarnsOnSeveralRevisions(t *testing.T) {
	src, _ := fetchSources(t, sourcesRun(
		revisionAction("jenkins.plugins.git.AbstractGitSCMSource$SCMRevisionImpl", "aaaa"),
		revisionAction("jenkins.plugins.git.AbstractGitSCMSource$SCMRevisionImpl", "bbbb"),
	))

	require.Len(t, src.PipelineRevisions, 2)
	require.Len(t, src.Warnings, 1)
	assert.Contains(t, src.Warnings[0], "2 SCMRevisionActions")
}

// Jenkins answers a tree query naming unknown fields with 200 and drops them,
// so _class has to be requested at both levels for an empty section to mean
// anything.
func TestBuildSourcesTreeNamesClassExplicitly(t *testing.T) {
	_, srv := fetchSources(t, sourcesRun())
	tree := srv.query.Get("tree")
	assert.Contains(t, tree, "actions[_class,")
	assert.Contains(t, tree, "revision[_class,hash]")
}

func TestGetBuildSourcesEmptyBuild(t *testing.T) {
	src, _ := fetchSources(t, sourcesRun())
	assert.Empty(t, src.Checkouts)
	assert.Empty(t, src.Libraries)
	assert.Empty(t, src.PipelineRevisions)
	assert.False(t, src.LibrariesActionPresent)
	assert.Equal(t, "my-app", src.Job)
	assert.Equal(t, 24, src.Build)
}
