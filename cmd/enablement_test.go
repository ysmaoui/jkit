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

// jobServer answers the state query with the given class and disabled value,
// recording every POST so a test can assert what was, or was not, sent.
func jobServer(t *testing.T, class string, disabled *bool, posts *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound) // CSRF disabled
			return
		}
		if r.Method == http.MethodPost {
			*posts = append(*posts, r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/disable") {
				v := true
				disabled = &v
			}
			if strings.HasSuffix(r.URL.Path, "/enable") {
				v := false
				disabled = &v
			}
			// Jenkins answers doDisable/doEnable with HttpRedirect("."), and the
			// client follows it like any browser would.
			w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/"+lastSegment(r.URL.Path)))
			w.WriteHeader(http.StatusFound)
			return
		}
		body := map[string]any{"_class": class}
		if disabled != nil {
			body["disabled"] = *disabled
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func boolp(b bool) *bool { return &b }

// A job type that does not implement supportsMakeDisabled omits the field
// entirely. That must be reported as the type having no such state, never as a
// permission problem or a missing job, and no POST may be sent.
func TestDisableRefusesAJobTypeWithNoEnabledState(t *testing.T) {
	var posts []string
	srv := jobServer(t, "com.cloudbees.hudson.plugins.folder.Folder", nil, &posts)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "disable", "team/svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no enabled state to change")
	assert.Empty(t, posts, "a type that cannot be disabled must not be posted to")
}

func TestDisableIsIdempotent(t *testing.T) {
	var posts []string
	srv := jobServer(t, "hudson.model.FreeStyleProject", boolp(true), &posts)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "disable", "team/svc")
	require.NoError(t, err)
	assert.Empty(t, posts, "an already-disabled job needs no POST")
}

func TestEnablePostsAndReportsTheResultingState(t *testing.T) {
	var posts []string
	srv := jobServer(t, "hudson.model.FreeStyleProject", boolp(true), &posts)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "enable", "team/svc", "--json")
	require.NoError(t, err)
	assert.Contains(t, posts, "/job/team/job/svc/enable")
	assert.Contains(t, out, `"supported": true`)
}

// A 403 must name the permission rather than reporting a generic denial, per
// the policy in DESIGN.md.
func TestDisableRefusalNamesThePermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_class": "hudson.model.FreeStyleProject", "disabled": false,
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "disable", "team/svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/Configure")
}

// Re-indexing rewrites a branch job's config, so a toggle there may not
// survive the next scan. Saying nothing would make this look permanent.
func TestDisableWarnsOnAMultibranchBranchChild(t *testing.T) {
	var posts []string
	srv := jobServer(t, "org.jenkinsci.plugins.workflow.job.WorkflowJob", boolp(false), &posts)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	stderr := captureStderr(t, func() {
		_, err := executeCmd(t, "disable", "team/svc/feature%2Fx")
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "next indexing run may undo this")
	assert.Contains(t, stderr, "team/svc")
}

func lastSegment(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path
	}
	return path[i+1:]
}
