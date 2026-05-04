package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestNormalizeJobPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"team/svc", "/job/team/job/svc"},
		{"svc", "/job/svc"},
		{"/team/svc/", "/job/team/job/svc"},
		{"a/b/c", "/job/a/job/b/job/c"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeJobPath(tt.input))
		})
	}
}

func TestAuthHeaderInjected(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Get("/api/json", nil)
	require.NoError(t, err)
	assert.Contains(t, gotAuth, "Basic ")
}

func TestCrumbFetchAndCache(t *testing.T) {
	var crumbCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			atomic.AddInt32(&crumbCalls, 1)
			_ = json.NewEncoder(w).Encode(crumbInfo{
				Crumb:             "test-crumb",
				CrumbRequestField: "Jenkins-Crumb",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	// First POST fetches crumb
	_, err := client.Post("/build", nil, "")
	require.NoError(t, err)
	// Second POST uses cached crumb
	_, err = client.Post("/build", nil, "")
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&crumbCalls))
}

func TestCrumbInvalidateOn403(t *testing.T) {
	var postCalls int32
	var crumbCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			atomic.AddInt32(&crumbCalls, 1)
			_ = json.NewEncoder(w).Encode(crumbInfo{
				Crumb:             "new-crumb",
				CrumbRequestField: "Jenkins-Crumb",
			})
			return
		}
		n := atomic.AddInt32(&postCalls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Post("/build", nil, "")
	require.NoError(t, err)
	// Should have fetched crumb twice (initial + after invalidation)
	assert.Equal(t, int32(2), atomic.LoadInt32(&crumbCalls))
}

func TestCheckResponse404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Get("/nonexistent", nil)
	require.Error(t, err)

	var nfe *jenkins.NotFoundError
	assert.ErrorAs(t, err, &nfe)
}

func TestCheckResponse401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "bad")
	_, err := client.Get("/api/json", nil)
	require.Error(t, err)

	var ae *jenkins.AuthError
	assert.ErrorAs(t, err, &ae)
}

func TestGetRetryOn503(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	resp, err := client.Get("/api/json", nil)
	require.NoError(t, err)
	CloseBody(resp)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestGetRetryMaxExceeded(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Get("/api/json", nil)
	require.Error(t, err)
	// All 4 attempts exhausted (initial + 3 retries), last 503 becomes ServerError
	var se *jenkins.ServerError
	assert.ErrorAs(t, err, &se)
	assert.Equal(t, int32(4), atomic.LoadInt32(&calls))
}

func TestPostNoRetryOn503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_ = json.NewEncoder(w).Encode(crumbInfo{
				Crumb:             "c",
				CrumbRequestField: "Jenkins-Crumb",
			})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Post("/build", nil, "")
	require.Error(t, err)

	var se *jenkins.ServerError
	assert.ErrorAs(t, err, &se)
}

func TestPostCrumbRetryOnly403(t *testing.T) {
	var crumbCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			atomic.AddInt32(&crumbCalls, 1)
			_ = json.NewEncoder(w).Encode(crumbInfo{
				Crumb:             "c",
				CrumbRequestField: "Jenkins-Crumb",
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "admin", "secret")
	_, err := client.Post("/build", nil, "")
	require.Error(t, err)

	var ae *jenkins.AuthError
	assert.ErrorAs(t, err, &ae)
	// Only 1 crumb fetch — no re-fetch since 401 is not 403
	assert.Equal(t, int32(1), atomic.LoadInt32(&crumbCalls))
}

func TestNormalizeJobPathSpecialChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"team/my job (copy)", "/job/team/job/my%20job%20%28copy%29"},
		{"folder/build & deploy", "/job/folder/job/build%20&%20deploy"},
		{"org/feature#1", "/job/org/job/feature%231"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeJobPath(tt.input))
		})
	}
}
