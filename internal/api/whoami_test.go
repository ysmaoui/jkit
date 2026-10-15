package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWhoAmIStatusDecodesIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/whoAmI/api/json", r.URL.Path)
		_, _ = w.Write([]byte(`{"name":"jane","anonymous":false,"authenticated":true}`))
	}))
	defer srv.Close()

	code, who, err := NewClient(srv.URL, "jane", "tok").WhoAmIStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 200, code)
	assert.Equal(t, &WhoAmI{Name: "jane"}, who)
}

func TestWhoAmIStatusUndecodableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>login</html>`))
	}))
	defer srv.Close()

	code, who, err := NewClient(srv.URL, "jane", "tok").WhoAmIStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 200, code)
	assert.Nil(t, who)
}

func TestWhoAmIStatusDoesNotFollowRedirect(t *testing.T) {
	var loginHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			loginHits.Add(1)
			_, _ = w.Write([]byte(`<html>login</html>`))
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()

	code, who, err := NewClient(srv.URL, "jane", "tok").WhoAmIStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.StatusFound, code)
	assert.Nil(t, who)
	assert.Zero(t, loginHits.Load())
}
