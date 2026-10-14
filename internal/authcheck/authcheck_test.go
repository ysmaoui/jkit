package authcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/config"
)

func statusServer(t *testing.T, code int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, "/whoAmI/api/json", r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"name":"admin","anonymous":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCheckResultKinds(t *testing.T) {
	tests := []struct {
		code       int
		wantResult string
	}{
		{http.StatusOK, ResultValid},
		{http.StatusUnauthorized, ResultInvalid},
		{http.StatusForbidden, ResultForbidden},
		{http.StatusInternalServerError, "http_500"},
		{http.StatusServiceUnavailable, "http_503"},
	}
	for _, tc := range tests {
		t.Run(tc.wantResult, func(t *testing.T) {
			srv, hits := statusServer(t, tc.code)
			result, code, _ := Check(context.Background(), srv.URL, "admin", "tok", time.Second)
			assert.Equal(t, tc.wantResult, result)
			assert.Equal(t, tc.code, code)
			assert.Equal(t, int32(1), hits.Load(), "one attempt, no retry")
		})
	}
}

func TestCheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	result, code, _ := Check(context.Background(), url, "admin", "tok", time.Second)
	assert.Equal(t, ResultUnreachable, result)
	assert.Zero(t, code)
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	result, _, _ := Check(context.Background(), srv.URL, "admin", "tok", 50*time.Millisecond)
	assert.Equal(t, ResultTimeout, result)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestCheckUnprovenAuthentication(t *testing.T) {
	tests := []struct {
		name, body string
		code       int
		wantResult string
	}{
		{"anonymous", `{"name":"anonymous","anonymous":true}`, 200, ResultAnonymous},
		{"no name", `{"anonymous":false}`, 200, ResultAnonymous},
		{"html page", `<html>login</html>`, 200, ResultAnonymous},
		{"redirect", "", http.StatusFound, "http_302"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					_, _ = w.Write([]byte(`<html>login</html>`))
					return
				}
				if tc.code == http.StatusFound {
					http.Redirect(w, r, "/login", tc.code)
					return
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			result, code, _ := Check(context.Background(), srv.URL, "admin", "tok", time.Second)
			assert.Equal(t, tc.wantResult, result)
			assert.Equal(t, tc.code, code)
		})
	}
	assert.True(t, Entry{Result: ResultAnonymous}.Failed())
}

func TestCheckAllNotesOtherIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"someone-else","anonymous":false}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{Hosts: map[string]*config.HostConfig{srv.URL: {User: "admin", Token: "t"}}}

	entries := CheckAll(context.Background(), cfg, Options{Timeout: time.Second})
	require.Len(t, entries, 1)
	assert.Equal(t, ResultValid, entries[0].Result)
	assert.Equal(t, []string{"authenticated as someone-else"}, entries[0].Problems)
	assert.Equal(t, "someone-else", entries[0].Identity)
	assert.False(t, entries[0].Failed())
}

func TestCheckAllBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"name":"admin","anonymous":false}`))
	}))
	t.Cleanup(srv.Close)
	hosts := map[string]*config.HostConfig{}
	for _, p := range []string{"a", "b", "c", "d", "e", "f"} {
		hosts[srv.URL+"/"+p] = &config.HostConfig{User: "admin", Token: "t"}
	}

	entries := CheckAll(context.Background(), &config.Config{Hosts: hosts}, Options{Timeout: time.Second, Concurrency: 2})
	for _, e := range entries {
		assert.Equal(t, ResultValid, e.Result)
	}
	assert.Equal(t, int32(2), peak.Load())
}

func TestCheckAllSortedWithProblems(t *testing.T) {
	ok, _ := statusServer(t, http.StatusOK)
	bad, _ := statusServer(t, http.StatusUnauthorized)
	cfg := &config.Config{Hosts: map[string]*config.HostConfig{
		bad.URL:       {User: "admin", Token: "t2", Alias: "x"},
		ok.URL:        {User: "admin", Token: "t1", Default: true},
		ok.URL + "/":  {User: "admin", Token: "t1", Alias: "x"},
		ok.URL + "#f": {User: "admin", Token: "t1"},
	}}

	entries := CheckAll(context.Background(), cfg, Options{Timeout: time.Second})
	require.Len(t, entries, 4)
	for i := 1; i < len(entries); i++ {
		assert.Less(t, entries[i-1].Host, entries[i].Host)
	}
	byHost := map[string]Entry{}
	for _, e := range entries {
		byHost[e.Host] = e
	}

	assert.Equal(t, ResultInvalid, byHost[bad.URL].Result)
	assert.True(t, byHost[bad.URL].Failed())
	assert.Equal(t, []string{"alias 'x' shared with " + ok.URL + "/"}, byHost[bad.URL].Problems)

	assert.Equal(t, ResultValid, byHost[ok.URL].Result)
	assert.True(t, byHost[ok.URL].Default)
	assert.Equal(t, []string{"duplicate of " + ok.URL + "#f, " + ok.URL + "/"}, byHost[ok.URL].Problems)

	assert.Contains(t, byHost[ok.URL+"/"].Problems, "not normalised: "+ok.URL)
	assert.Contains(t, byHost[ok.URL+"#f"].Problems, "not normalised: "+ok.URL)
}

func TestEntryFailedAndUnverified(t *testing.T) {
	assert.False(t, Entry{Result: ResultValid}.Failed())
	assert.False(t, Entry{Result: ResultValid}.Unverified())
	assert.True(t, Entry{Result: ResultForbidden}.Failed())
	assert.True(t, Entry{Result: ResultTimeout}.Unverified())
	assert.True(t, Entry{Result: "http_500"}.Unverified())
	assert.True(t, Entry{Result: ResultValid, KeyProblems: []config.KeyProblem{{Kind: config.ProblemDuplicateKey}}}.Failed())
}
