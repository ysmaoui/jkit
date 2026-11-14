package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const apiJSON = `{"jobs":[{"name":"my-app","color":"blue"}]}`

func apiServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
	return srv
}

func withTerminal(t *testing.T, tty bool) {
	t.Helper()
	old := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdoutIsTerminal = old })
}

func jsonHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	_, _ = w.Write([]byte(apiJSON))
}

func TestAPIPrintsRawBodyAndKeepsQuery(t *testing.T) {
	withTerminal(t, false)
	var gotPath, gotQuery string
	apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		jsonHandler(w, r)
	})

	out, err := executeCmd(t, "api", "computer/api/json?tree=computer[displayName,offline]")
	require.NoError(t, err)
	assert.Equal(t, apiJSON, out)
	assert.Equal(t, "/computer/api/json", gotPath)
	assert.Equal(t, "tree=computer[displayName,offline]", gotQuery)
}

func TestAPIPrettyPrintsOnTerminal(t *testing.T) {
	withTerminal(t, true)
	apiServer(t, jsonHandler)

	out, err := executeCmd(t, "api", "/api/json")
	require.NoError(t, err)
	assert.Contains(t, out, "{\n  \"jobs\": [\n")
}

func TestAPIJSONFlag(t *testing.T) {
	withTerminal(t, false)
	apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/text" {
			_, _ = w.Write([]byte("plain text"))
			return
		}
		jsonHandler(w, r)
	})

	out, err := executeCmd(t, "api", "/api/json", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, "{\n  \"jobs\": [\n")

	_, err = executeCmd(t, "api", "/text", "--json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not JSON")
}

func TestAPIFormatTemplatesDecodedBody(t *testing.T) {
	apiServer(t, jsonHandler)

	out, err := executeCmd(t, "api", "/api/json", "--format", "{{range .jobs}}{{.name}}={{.color}}{{end}}")
	require.NoError(t, err)
	assert.Equal(t, "my-app=blue", out)
}

func TestAPIPostSendsCrumbAndHeaders(t *testing.T) {
	var method, crumb, extra, accept string
	apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_, _ = w.Write([]byte(`{"crumb":"abc","crumbRequestField":"Jenkins-Crumb"}`))
			return
		}
		method, crumb = r.Method, r.Header.Get("Jenkins-Crumb")
		extra, accept = r.Header.Get("X-Extra"), r.Header.Get("Accept")
		w.WriteHeader(http.StatusCreated)
	})

	_, err := executeCmd(t, "api", "-X", "post", "/job/my-app/build", "-H", "X-Extra: one", "--header", "Accept:text/plain")
	require.NoError(t, err)
	assert.Equal(t, "POST", method)
	assert.Equal(t, "abc", crumb)
	assert.Equal(t, "one", extra)
	assert.Equal(t, "text/plain", accept)
}

func TestAPIRejectsMalformedHeader(t *testing.T) {
	apiServer(t, jsonHandler)
	_, err := executeCmd(t, "api", "/api/json", "-H", "no-colon")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Key: Value")
}

func TestAPIIncludePrintsStatusAndHeaders(t *testing.T) {
	withTerminal(t, false)
	apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Jenkins", "2.500")
		jsonHandler(w, r)
	})

	out, err := executeCmd(t, "api", "/api/json", "-i")
	require.NoError(t, err)
	assert.Regexp(t, `^HTTP/1\.1 200 OK\n`, out)
	assert.Contains(t, out, "X-Jenkins: 2.500\n")
	assert.Contains(t, out, "\n\n"+apiJSON)
}

func TestAPIErrorStatusPrintsBodyAndTypedError(t *testing.T) {
	withTerminal(t, false)
	for _, tc := range []struct {
		status int
		check  func(t *testing.T, err error)
	}{
		{http.StatusForbidden, func(t *testing.T, err error) {
			var e *jenkins.PermissionError
			assert.ErrorAs(t, err, &e)
		}},
		{http.StatusNotFound, func(t *testing.T, err error) {
			var e *jenkins.NotFoundError
			require.ErrorAs(t, err, &e)
			assert.Contains(t, err.Error(), "check the path")
			assert.NotContains(t, err.Error(), "jkit list")
		}},
		{http.StatusInternalServerError, func(t *testing.T, err error) {
			var e *jenkins.ServerError
			require.ErrorAs(t, err, &e)
			assert.Equal(t, 500, e.StatusCode)
		}},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			apiServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("error page"))
			})
			out, err := executeCmd(t, "api", "/thing", "--json", "--format", "{{.x}}")
			require.Error(t, err)
			tc.check(t, err)
			assert.Equal(t, "error page", out)
		})
	}
}

func TestAPIFullURL(t *testing.T) {
	withTerminal(t, false)
	srv := apiServer(t, jsonHandler)

	out, err := executeCmd(t, "api", srv.URL+"/api/json")
	require.NoError(t, err)
	assert.Equal(t, apiJSON, out)

	out, err = executeCmd(t, "api", "--host", srv.URL, srv.URL+"/api/json")
	require.NoError(t, err)
	assert.Equal(t, apiJSON, out)

	_, err = executeCmd(t, "api", "https://jenkins.example.com/api/json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jkit auth login")

	_, err = executeCmd(t, "api", "--host", srv.URL, "https://jenkins.example.com/api/json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not on")
}

// sentRequest is what bodyServer saw on the last non-crumb request.
type sentRequest struct {
	method, contentType, crumb, body string
}

func bodyServer(t *testing.T) *sentRequest {
	t.Helper()
	got := &sentRequest{}
	apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_, _ = w.Write([]byte(`{"crumb":"abc","crumbRequestField":"Jenkins-Crumb"}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		*got = sentRequest{r.Method, r.Header.Get("Content-Type"), r.Header.Get("Jenkins-Crumb"), string(b)}
	})
	return got
}

func TestAPIFormBody(t *testing.T) {
	withTerminal(t, false)
	got := bodyServer(t)

	_, err := executeCmd(t, "api", "/manage/log/newLogRecorder",
		"-d", "a=1",
		"--data-urlencode", "name=my recorder&x=y+z",
		"-d", "raw=p+q",
		"--data-urlencode", "only value",
		"--data-urlencode", "=a b=c")
	require.NoError(t, err)
	assert.Equal(t, "POST", got.method)
	assert.Equal(t, "abc", got.crumb)
	assert.Equal(t, "application/x-www-form-urlencoded", got.contentType)
	assert.Equal(t, "a=1&name=my+recorder%26x%3Dy%2Bz&raw=p+q&only+value&a+b%3Dc", got.body)
}

func TestAPIFormBodyKeepsContentTypeHeader(t *testing.T) {
	withTerminal(t, false)
	got := bodyServer(t)

	_, err := executeCmd(t, "api", "/x", "-d", "a=1", "-H", "content-type: text/plain")
	require.NoError(t, err)
	assert.Equal(t, "text/plain", got.contentType)
	assert.Equal(t, "a=1", got.body)
}

func TestAPIInputFile(t *testing.T) {
	withTerminal(t, false)
	got := bodyServer(t)
	path := filepath.Join(t.TempDir(), "config.xml")
	require.NoError(t, os.WriteFile(path, []byte("<project/>"), 0o600))

	_, err := executeCmd(t, "api", "/job/my-app/config.xml", "--input", path, "-H", "Content-Type: application/xml")
	require.NoError(t, err)
	assert.Equal(t, "POST", got.method)
	assert.Equal(t, "abc", got.crumb)
	assert.Equal(t, "application/xml", got.contentType)
	assert.Equal(t, "<project/>", got.body)
}

func TestAPIInputStdin(t *testing.T) {
	withTerminal(t, false)
	got := bodyServer(t)
	rootCmd.SetIn(strings.NewReader("raw & body"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	_, err := executeCmd(t, "api", "-X", "PUT", "/x", "--input", "-")
	require.NoError(t, err)
	assert.Equal(t, "PUT", got.method)
	assert.Empty(t, got.contentType)
	assert.Equal(t, "raw & body", got.body)
}

func TestAPIBodyErrors(t *testing.T) {
	got := bodyServer(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-X", "GET", "-d", "a=1"}, "cannot be sent with GET"},
		{[]string{"-X", "head", "--data-urlencode", "a=1"}, "cannot be sent with HEAD"},
		{[]string{"--input", "-", "-d", "a=1"}, "--input cannot be used"},
	} {
		_, err := executeCmd(t, append([]string{"api", "/x"}, tc.args...)...)
		require.Error(t, err, tc.args)
		assert.Contains(t, err.Error(), tc.want, tc.args)
	}
	assert.Empty(t, got.method, "no request may reach the server")
}
