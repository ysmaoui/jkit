package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestRawKeepsQueryBytes(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "u", "t")
	for _, target := range []string{
		"/computer/api/json?tree=computer[displayName,offline]{0,2}",
		"computer/api/json?tree=computer[displayName,offline]{0,2}",
		srv.URL + "/computer/api/json?tree=computer[displayName,offline]{0,2}",
	} {
		resp, err := c.Raw(context.Background(), "GET", target, nil)
		require.NoError(t, err, target)
		CloseBody(resp)
		assert.Equal(t, "/computer/api/json", gotPath, target)
		assert.Equal(t, "tree=computer[displayName,offline]{0,2}", gotQuery, target)
	}
}

func TestRawReturnsErrorResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such thing", http.StatusNotFound)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "GET", "/nope", nil)
	require.NoError(t, err)
	defer CloseBody(resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "no such thing")
	var nf *jenkins.NotFoundError
	assert.ErrorAs(t, StatusError(resp, body), &nf)
}

func TestRawPostSendsCrumbAndRefetchesStale(t *testing.T) {
	crumbFetches := 0
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crumbIssuer/api/json":
			crumbFetches++
			if crumbFetches == 1 {
				_, _ = w.Write([]byte(`{"crumb":"stale","crumbRequestField":"Jenkins-Crumb"}`))
				return
			}
			_, _ = w.Write([]byte(`{"crumb":"fresh","crumbRequestField":"Jenkins-Crumb"}`))
		case "/quietDown":
			posted = append(posted, r.Header.Get("Jenkins-Crumb"))
			if r.Header.Get("Jenkins-Crumb") != "fresh" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			assert.Equal(t, "yes", r.Header.Get("X-Extra"))
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()

	h := http.Header{}
	h.Set("X-Extra", "yes")
	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "POST", "/quietDown", h)
	require.NoError(t, err)
	defer CloseBody(resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"stale", "fresh"}, posted)
}

func TestRawGetSendsNoCrumb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEqual(t, "/crumbIssuer/api/json", r.URL.Path)
		assert.NotEmpty(t, r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "GET", "/api/json", nil)
	require.NoError(t, err)
	CloseBody(resp)
}

func TestRawRefusesURLOffHost(t *testing.T) {
	c := NewClient("https://jenkins.example.com/ci", "u", "t")
	for _, target := range []string{
		"https://other.example.com/api/json",
		"https://jenkins.example.com/api/json",
		"https://jenkins.example.com/cifoo/api/json",
		"http://jenkins.example.com/ci/api/json",
	} {
		_, err := c.Raw(context.Background(), "GET", target, nil)
		require.Error(t, err, target)
		assert.Contains(t, err.Error(), "is not on", target)
	}
}

func TestRawPostDoesNotFollowRedirects(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/crumbIssuer/api/json":
			http.NotFound(w, r)
		case "/stop", "/old":
			http.Redirect(w, r, "/landing", http.StatusFound)
		default:
			_, _ = w.Write([]byte("landing page"))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "t")

	resp, err := c.Raw(context.Background(), "POST", "/stop", nil)
	require.NoError(t, err)
	CloseBody(resp)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/landing", resp.Header.Get("Location"))

	resp, err = c.Raw(context.Background(), "GET", "/old", nil)
	require.NoError(t, err)
	CloseBody(resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"GET /crumbIssuer/api/json", "POST /stop", "GET /old", "GET /landing"}, methods)
}

func TestRawPostWithoutCrumbIsNotRetried(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			http.NotFound(w, r)
			return
		}
		posts++
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "POST", "/x", nil)
	require.NoError(t, err)
	defer CloseBody(resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, 1, posts)
}

func TestRawPostSentWhenCrumbFetchFails(t *testing.T) {
	var crumbHeader []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		crumbHeader = append(crumbHeader, r.Header.Get("Jenkins-Crumb"))
		http.Error(w, "unauthorized body", http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "POST", "/x", nil)
	require.NoError(t, err)
	defer CloseBody(resp)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "unauthorized body")
	var ae *jenkins.AuthError
	assert.ErrorAs(t, StatusError(resp, body), &ae)
	assert.Equal(t, []string{""}, crumbHeader)
}

func TestRawPostForbiddenTwice(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_, _ = w.Write([]byte(`{"crumb":"c","crumbRequestField":"Jenkins-Crumb"}`))
			return
		}
		posted = append(posted, r.Header.Get("Jenkins-Crumb"))
		http.Error(w, "no permission", http.StatusForbidden)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, "u", "t").Raw(context.Background(), "POST", "/x", nil)
	require.NoError(t, err)
	defer CloseBody(resp)
	assert.Equal(t, []string{"c", "c"}, posted)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "no permission")
	var pe *jenkins.PermissionError
	assert.ErrorAs(t, StatusError(resp, body), &pe)
}
