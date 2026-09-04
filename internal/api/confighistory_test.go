package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const configHistoryBody = `{"_class":"hudson.plugins.jobConfigHistory.JobConfigHistoryProjectAction",
 "jobConfigHistory":[
  {"changeReasonComment":"bumped the timeout","currentName":"","date":"2026-08-27_14-58-13","hasConfig":true,
   "job":"team/svc","oldName":"","operation":"Changed","user":"Ada Lovelace","userID":"ada"},
  {"changeReasonComment":null,"currentName":"","date":"2026-07-24_09-56-57","hasConfig":true,
   "job":"team/svc","oldName":"","operation":"Created","user":"SYSTEM","userID":"SYSTEM"}]}`

func TestGetJobConfigHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/job/team/job/svc/jobConfigHistory/api/json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(configHistoryBody))
	}))
	defer srv.Close()

	entries, err := NewClient(srv.URL, "admin", "secret").GetJobConfigHistory("team/svc")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "2026-08-27_14-58-13", entries[0].Date)
	assert.Equal(t, "Changed", entries[0].Operation)
	assert.Equal(t, "Ada Lovelace", entries[0].User)
	assert.Equal(t, "ada", entries[0].UserID)
	assert.Equal(t, "bumped the timeout", entries[0].Reason())
	assert.False(t, entries[0].BySystem())
	assert.True(t, entries[1].BySystem())
	assert.Empty(t, entries[1].Reason(), "a null changeReasonComment must not panic or print null")
}

// Insufficient permission is answered with 200 and an empty array, not 403, so
// an empty list must reach the caller as a normal result to be explained.
func TestGetJobConfigHistoryEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"_class":"hudson.plugins.jobConfigHistory.JobConfigHistoryProjectAction","jobConfigHistory":[]}`))
	}))
	defer srv.Close()

	entries, err := NewClient(srv.URL, "admin", "secret").GetJobConfigHistory("team/svc")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// The two 404s that must not be confused. A Jenkins without the plugin and a
// Jenkins without the job serve the same generic 404 for /jobConfigHistory;
// only the job's own endpoint tells them apart.
func TestGetJobConfigHistoryDistinguishesTheTwo404s(t *testing.T) {
	t.Run("plugin missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/jobConfigHistory/") {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("<html><head><title>Error 404 Not Found</title></head></html>"))
				return
			}
			_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc"}`))
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigHistory("team/svc")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JobConfigHistory")
		var nfe *jenkins.NotFoundError
		assert.False(t, errors.As(err, &nfe), "a missing plugin must not be reported as a missing job")
		assert.NotContains(t, err.Error(), "jkit list")
	})

	t.Run("job missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html><head><title>Error 404 Not Found</title></head></html>"))
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigHistory("team/gone")
		require.Error(t, err)
		var nfe *jenkins.NotFoundError
		require.ErrorAs(t, err, &nfe)
		assert.Equal(t, "job", nfe.Resource)
		assert.Equal(t, "team/gone", nfe.Name)
		assert.NotContains(t, err.Error(), "JobConfigHistory")
	})
}

// A branch job is one job whose name contains slashes; the request must address
// it as a single path segment (the wire form is double-escaped, so the server
// sees the branch name back).
func TestGetJobConfigHistoryBranchJobPath(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(`{"jobConfigHistory":[]}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigHistory("team/svc/feature%2Fbuild")
	require.NoError(t, err)
	assert.Equal(t, "/job/team/job/svc/job/feature%2Fbuild/jobConfigHistory/api/json", got)
}

func TestGetJobConfigRevision(t *testing.T) {
	var got *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL
		_, _ = w.Write([]byte("<flow-definition/>\n"))
	}))
	defer srv.Close()

	data, err := NewClient(srv.URL, "admin", "secret").GetJobConfigRevision("team/svc", "2026-07-24_13-06-30")
	require.NoError(t, err)
	assert.Equal(t, "<flow-definition/>\n", string(data))
	assert.Equal(t, "/job/team/job/svc/jobConfigHistory/configOutput", got.Path)
	assert.Equal(t, "2026-07-24_13-06-30", got.Query().Get("timestamp"))
	assert.Equal(t, "raw", got.Query().Get("type"))
}

// Trap 1 of jk-2gc.6: the plugin answers 200 with a zero-byte body for a
// timestamp it does not hold, so the status code proves nothing and the error
// has to come from the empty response, naming the timestamp that produced it.
func TestGetJobConfigRevisionEmptyBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigRevision("team/svc", "1999-01-01_00-00-00")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1999-01-01_00-00-00")
	assert.Contains(t, err.Error(), "team/svc")
	assert.Contains(t, err.Error(), "--history")
}

// A whitespace-only body is the same failure: nothing was stored.
func TestGetJobConfigRevisionBlankBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("\n  \n"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigRevision("team/svc", "2026-07-24_13-06-30")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2026-07-24_13-06-30")
}

// The plugin's own 404 must still be told apart from a missing job.
func TestGetJobConfigRevisionMissingPlugin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/jobConfigHistory/") {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html><head><title>Error 404 Not Found</title></head></html>"))
			return
		}
		_, _ = w.Write([]byte(`{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc"}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "admin", "secret").GetJobConfigRevision("team/svc", "2026-07-24_13-06-30")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JobConfigHistory")
}
