package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourcesLogServer answers the action tree and the console. It records whether
// the console was ever fetched, because not fetching it is part of the contract:
// a build whose libraries all resolve by branch name must cost no log request.
type sourcesLogServer struct {
	*httptest.Server
	logFetched bool
}

func newSourcesLogServer(t *testing.T, actions []map[string]any, console string) *sourcesLogServer {
	t.Helper()
	s := &sourcesLogServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "progressiveText"):
			s.logFetched = true
			w.Header().Set("X-Text-Size", fmt.Sprint(len(console)))
			_, _ = fmt.Fprint(w, console)
		case strings.HasSuffix(r.URL.Path, "/job/my-app/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "my-app", "lastBuild": map[string]any{"number": 45},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"actions": actions})
		}
	}))
	t.Cleanup(s.Close)
	setupTestConfig(t, s.URL)
	return s
}

// ambiguousActions is the shape of the build the feature exists for: two
// libraries both requesting "master", each repository checked out, and nothing
// in either action saying which checkout is whose.
func ambiguousActions() []map[string]any {
	return []map[string]any{
		gitBuildData("https://git.example.com/swf/tools-global-shared-lib-src.git",
			"327b5c22a40ee9ef448b917b2b4a5b22e8d63239", "master"),
		gitBuildData("https://git.example.com/swf/tools-premium-shared-lib-src.git",
			"f5d759ea6d051759908fedaf684e52bdfbdf0c2c", "master"),
		libraries(
			map[string]any{"name": "global-lib", "version": "master", "trusted": true},
			map[string]any{"name": "premium-lib", "version": "master", "trusted": true},
		),
	}
}

const ambiguousConsole = `[2026-09-13T19:00:07.813Z] Loading library global-lib@master
[2026-09-13T19:00:08.278Z]  > git ls-remote -- https://git.example.com/swf/tools-global-shared-lib-src.git # timeout=10
[2026-09-13T19:00:08.678Z] Found match: refs/heads/master revision 327b5c22a40ee9ef448b917b2b4a5b22e8d63239
[2026-09-13T19:00:09.845Z] Loading library premium-lib@master
[2026-09-13T19:00:09.859Z]  > git ls-remote -- https://git.example.com/swf/tools-premium-shared-lib-src.git # timeout=10
[2026-09-13T19:00:10.228Z] Found match: refs/heads/master revision f5d759ea6d051759908fedaf684e52bdfbdf0c2c
`

func TestSourcesResolvesAmbiguousLibrariesFromTheLog(t *testing.T) {
	srv := newSourcesLogServer(t, ambiguousActions(), ambiguousConsole)

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.True(t, srv.logFetched, "an unresolved library must send jkit to the console")
	assert.NotContains(t, out, "SHA not resolvable")
	assert.Contains(t, out, "327b5c22a40ee9ef448b917b2b4a5b22e8d63239")
	assert.Contains(t, out, "f5d759ea6d051759908fedaf684e52bdfbdf0c2c")
	assert.Contains(t, out, "recorded in the build log when the library was loaded")
	// The cross-check attributes the checkouts, as a branch match already does.
	assert.Contains(t, out, "-> shared library global-lib")
	assert.Contains(t, out, "-> shared library premium-lib")
}

// The console is large and reading it is the expensive part. A build whose
// libraries all match a checkout by branch name must not pay for it.
func TestSourcesReadsNoLogWhenEverythingResolves(t *testing.T) {
	actions := []map[string]any{
		gitBuildData("https://git.example.com/CARIAD/tools-sdk-global-jenkins-shared-lib.git",
			"e3fc9e5c9c9ab32e6318ea088d67c6c2b29b7aa0", "develop"),
		gitBuildData("https://git.example.com/CARIAD/tools-hera-2.git",
			"52d790ee106971aca82863770dbb464f503de21a", "feature/bazel-remote-exec"),
		libraries(
			map[string]any{"name": "e3-sdk-global-jenkins-shared-lib", "version": "develop", "trusted": true},
			map[string]any{"name": "hera2", "version": "feature/bazel-remote-exec", "trusted": false},
		),
	}
	srv := newSourcesLogServer(t, actions, "")

	out, err := executeCmd(t, "sources", "my-app", "24")
	require.NoError(t, err)
	assert.NotContains(t, out, "SHA not resolvable")
	assert.False(t, srv.logFetched, "nothing was unresolved, so the console must not be fetched")
}

// A console that resolves nothing leaves the existing refusal standing. The
// previous answer was honest and stays.
func TestSourcesKeepsRefusalWhenTheLogSaysNothing(t *testing.T) {
	srv := newSourcesLogServer(t, ambiguousActions(), "[2026-09-13T19:00:07.813Z] nothing of interest here\n")

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.True(t, srv.logFetched)
	assert.Contains(t, out, "SHA not resolvable")
	assert.Contains(t, out, "the build log recorded nothing better")
}

// A library the log resolved twice, to different commits, must not be reported
// at either: the build ran both, and naming one is naming code that the rest of
// the build did not use.
func TestSourcesRefusesALibraryResolvedTwice(t *testing.T) {
	console := ambiguousConsole +
		"[2026-09-13T19:05:00.000Z] Loading library global-lib@master\n" +
		"[2026-09-13T19:05:00.100Z]  > git ls-remote -- https://git.example.com/swf/tools-global-shared-lib-src.git # timeout=10\n" +
		"[2026-09-13T19:05:00.200Z] Found match: refs/heads/master revision " + strings.Repeat("c", 40) + "\n"
	newSourcesLogServer(t, ambiguousActions(), console)

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.Contains(t, out, "resolved \"global-lib\" more than once")
	assert.NotContains(t, out, strings.Repeat("c", 40))
	// The other library is unaffected by its neighbour's ambiguity.
	assert.Contains(t, out, "f5d759ea6d051759908fedaf684e52bdfbdf0c2c")
}

// The log and BuildData disagreeing about the same repository is a fact the
// reader needs; preferring one silently would hide it.
func TestSourcesSurfacesLogVersusCheckoutDisagreement(t *testing.T) {
	actions := ambiguousActions()
	console := strings.Replace(ambiguousConsole,
		"327b5c22a40ee9ef448b917b2b4a5b22e8d63239", strings.Repeat("e", 40), 1)
	newSourcesLogServer(t, actions, console)

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.Contains(t, out, "but the git checkout of the same repository recorded")
	assert.Contains(t, out, "327b5c22a40ee9ef448b917b2b4a5b22e8d63239")
}

// An unreadable console is not fatal. The report stands, and says why it could
// not be improved.
func TestSourcesSurvivesAnUnreadableLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "progressiveText") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"actions": ambiguousActions()})
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.Contains(t, out, "SHA not resolvable")
	assert.Contains(t, out, "could not read the console")
}

func TestSourcesJSONCarriesTheLogResolution(t *testing.T) {
	newSourcesLogServer(t, ambiguousActions(), ambiguousConsole)

	out, err := executeCmd(t, "sources", "my-app", "45", "--json")
	require.NoError(t, err)
	var got struct {
		Libraries []struct {
			Name       string `json:"name"`
			SHA1       string `json:"sha1"`
			Resolution string `json:"resolution"`
		} `json:"libraries"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Libraries, 2)
	for _, l := range got.Libraries {
		assert.Equal(t, "matched-in-build-log", l.Resolution, l.Name)
		assert.Len(t, l.SHA1, 40, l.Name)
	}
}

// A url read out of the console goes through the same credential redaction as
// one read from a checkout. An ls-remote line is a place a token really does
// turn up.
func TestSourcesRedactsACredentialFromTheLog(t *testing.T) {
	console := strings.Replace(ambiguousConsole,
		"https://git.example.com/swf/tools-global-shared-lib-src.git",
		"https://bob:s3cr3t-token@git.example.com/swf/tools-global-shared-lib-src.git", 1)
	newSourcesLogServer(t, ambiguousActions(), console)

	out, err := executeCmd(t, "sources", "my-app", "45")
	require.NoError(t, err)
	assert.NotContains(t, out, "s3cr3t-token", "a credential in an ls-remote url must not reach the report")
	assert.Contains(t, out, "***@git.example.com")
}
