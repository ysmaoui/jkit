package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("../internal/jenkins/testdata/" + fixture)
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body)
	}))
}

func inspect(t *testing.T, fixture string, args ...string) string {
	t.Helper()
	srv := configServer(t, fixture)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, append([]string{"inspect"}, args...)...)
	require.NoError(t, err)
	return out
}

func TestInspectMultibranch(t *testing.T) {
	out := inspect(t, "config_multibranch.xml", "team/widget")

	assert.Contains(t, out, "multibranch pipeline")
	assert.Contains(t, out, "Jenkinsfile")
	assert.Contains(t, out, "ACME/widget")
	assert.Contains(t, out, "https://github.example.com/api/v3")
	assert.Contains(t, out, "acme-github-app")
	assert.Contains(t, out, "H H/4 * * *")
	assert.Contains(t, out, "30 days")
	assert.Contains(t, out, "20 builds")

	// Magic numbers must reach the reader as words.
	assert.Contains(t, out, "all branches")
	assert.NotContains(t, out, "strategyId=3\n")
	assert.Contains(t, out, "merged with the current target branch")
	assert.Contains(t, out, "trust TrustPermission")
}

// An undecoded trait must appear in the rendered output. If it were dropped,
// the discovery section would silently misdescribe why a branch does not build.
func TestInspectShowsUndecodedTraitClass(t *testing.T) {
	out := inspect(t, "config_multibranch.xml", "team/widget")
	assert.Contains(t, out, "jenkins.plugins.git.traits.CloneOptionTrait")
	assert.Contains(t, out, "not decoded")
}

func TestInspectBranchChildPointsAtParent(t *testing.T) {
	out := inspect(t, "config_branch.xml", "team/widget/feature%2Fbuild-tweak")
	assert.Contains(t, out, "jkit inspect team/widget")
	assert.Contains(t, out, "https://github.example.com/ACME/widget.git")
	// The parent holds the discovery rules; claiming this job discovers nothing
	// would be wrong.
	assert.NotContains(t, out, "no traits are configured")
	assert.NotContains(t, out, "none are configured")
}

func TestInspectDisabledJob(t *testing.T) {
	out := inspect(t, "config_inline_pipeline.xml", "my-app")
	assert.Contains(t, out, "DISABLED")
	assert.Contains(t, out, "inline in the job config")
}

func TestInspectFolder(t *testing.T) {
	out := inspect(t, "config_folder.xml", "team")
	assert.Contains(t, out, "folder")
	assert.Contains(t, out, "inspect a job inside it")
}

// A freestyle job builds by itself. Telling the reader to look inside it, as
// the container hint used to, sends them after a job that does not exist.
func TestInspectFreestyleIsNotTreatedAsAContainer(t *testing.T) {
	out := inspect(t, "config_freestyle.xml", "team/legacy")
	assert.Contains(t, out, "Type:         freestyle")
	assert.NotContains(t, out, "inspect a job inside it")
	assert.Contains(t, out, "does not read the build steps")
}

// Every source must be rendered: if the branch in question comes from the
// second one, a report of only the first describes the wrong repository.
func TestInspectRendersEveryBranchSource(t *testing.T) {
	out := inspect(t, "config_two_sources.xml", "team/widget")
	assert.Contains(t, out, "Repository 1 of 2")
	assert.Contains(t, out, "Repository 2 of 2")
	assert.Contains(t, out, "ACME/widget")
	assert.Contains(t, out, "ACME/gadget")
	assert.Contains(t, out, "Bitbucket")

	// The second source discovers nothing and builds everything but tags, and
	// both of those follow from a section being absent rather than present.
	assert.Contains(t, out, "no traits are configured, so this source discovers no branch, PR or tag")
	assert.Contains(t, out, "none are configured, so every discovered branch and PR builds when it changes, and tags never build")
}

// An organization folder's navigator traits decide discovery for every child
// job it creates, so dropping them and then claiming the folder holds no
// definition is wrong twice over.
func TestInspectOrganizationFolder(t *testing.T) {
	out := inspect(t, "config_org_folder.xml", "acme")
	assert.Contains(t, out, "organization folder")
	assert.Contains(t, out, "Organization navigator")
	assert.Contains(t, out, "ci/Jenkinsfile")
	assert.Contains(t, out, "branches that are not also filed as PRs")
	assert.Contains(t, out, `only repositories matching regex "widget-.*" are indexed`)
	assert.Contains(t, out, "Skip initial build on first branch indexing")
	assert.NotContains(t, out, "no pipeline definition of its own")
}

func TestInspectNamedBranchFiltersAndImpossibleTagWindow(t *testing.T) {
	out := inspect(t, "config_named_branches.xml", "team/widget")
	assert.Contains(t, out, `name matches regex "release/.*"`)
	assert.Contains(t, out, `name is exactly "main", ignoring case`)
	assert.Contains(t, out, "no tag ever builds")
	assert.Contains(t, out, "never starts a build")
	assert.Contains(t, out, "not when the only change is the target branch moving")
}

// A control character in a description used to abort the whole command.
func TestInspectSurvivesControlCharacters(t *testing.T) {
	out := inspect(t, "config_control_chars.xml", "team/widget")
	assert.Contains(t, out, "Pasted from a coloured terminal")
	assert.Contains(t, out, "inline in the job config")
}

func TestInspectJSON(t *testing.T) {
	out := inspect(t, "config_multibranch.xml", "team/widget", "--json")

	var got struct {
		Kind      string `json:"kind"`
		Container bool   `json:"container"`
		Sources   []struct {
			Kind   string `json:"kind"`
			Source struct {
				RepoOwner  string `json:"repoOwner"`
				Repository string `json:"repository"`
			} `json:"source"`
			Traits []struct {
				Class   string `json:"class"`
				Meaning string `json:"meaning"`
				Decoded bool   `json:"decoded"`
				Raw     string `json:"raw"`
			} `json:"traits"`
		} `json:"sources"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "multibranch pipeline", got.Kind)
	assert.True(t, got.Container)
	require.Len(t, got.Sources, 1)
	assert.Equal(t, "ACME", got.Sources[0].Source.RepoOwner)
	assert.Equal(t, "widget", got.Sources[0].Source.Repository)

	var undecoded int
	for _, tr := range got.Sources[0].Traits {
		if !tr.Decoded {
			undecoded++
		}
	}
	assert.Len(t, got.Sources[0].Traits, 6, "every trait element must be present, decoded or not")
	assert.Equal(t, 1, undecoded)
}

// The output is what someone reads to explain a failure, so nothing in it may
// be a class name a human has to decode themselves without being told so.
func TestInspectMarksEveryUndecodedLine(t *testing.T) {
	out := inspect(t, "config_multibranch.xml", "team/widget")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "CloneOptionTrait") {
			assert.True(t, strings.HasPrefix(line, "! "), "an undecoded trait must be flagged: %q", line)
		}
	}
}
