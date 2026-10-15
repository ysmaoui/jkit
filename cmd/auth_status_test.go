package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const authTestToken = "tok-a7f3c9e1-never-print"

func authServer(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"name":"admin","anonymous":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type authHost struct {
	key, alias string
	isDefault  bool
}

func writeAuthConfig(t *testing.T, hosts ...authHost) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	var b strings.Builder
	b.WriteString("hosts:\n")
	for _, h := range hosts {
		fmt.Fprintf(&b, "  %q:\n    user: admin\n    token: %s\n", h.key, authTestToken)
		if h.alias != "" {
			fmt.Fprintf(&b, "    alias: %s\n", h.alias)
		}
		if h.isDefault {
			b.WriteString("    default: true\n")
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte(b.String()), 0600))
}

func runAuthStatus(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout, err = executeCmd(t, append([]string{"auth", "status"}, args...)...)
	})
	if strings.Contains(stdout+stderr, authTestToken) {
		t.Fatal("token appears in command output")
	}
	if err != nil && strings.Contains(err.Error(), authTestToken) {
		t.Fatal("token appears in command error")
	}
	return stdout, stderr, err
}

func requireExit1(t *testing.T, err error) {
	t.Helper()
	var exitErr *jenkins.ExitError
	require.True(t, errors.As(err, &exitErr), "want ExitError, got %v", err)
	assert.Equal(t, 1, exitErr.Code)
}

func TestAuthStatusAllValid(t *testing.T) {
	a, b := authServer(t, 200), authServer(t, 200)
	writeAuthConfig(t, authHost{key: a, alias: "prod", isDefault: true}, authHost{key: b})

	out, stderr, err := runAuthStatus(t, "--all")
	require.NoError(t, err)
	assert.Contains(t, stderr, "Checking 2 host(s)")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3)
	assert.Regexp(t, `^HOST\s+ALIAS\s+USER\s+DEFAULT\s+RESULT\s+NOTES`, lines[0])
	assert.Regexp(t, `^`+regexp.QuoteMeta(a)+`\s+prod\s+admin\s+yes\s+valid\s*$`, rowFor(lines, a))
	assert.Regexp(t, `^`+regexp.QuoteMeta(b)+`\s+-\s+admin\s+valid\s*$`, rowFor(lines, b))
	assert.NotContains(t, stderr, "warning")
}

func rowFor(lines []string, host string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, host+" ") {
			return l
		}
	}
	return ""
}

func TestAuthStatusAllAnonymousExit1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"anonymous","anonymous":true}`))
	}))
	t.Cleanup(srv.Close)
	writeAuthConfig(t, authHost{key: srv.URL, isDefault: true})

	out, _, err := runAuthStatus(t, "--all", "--json")
	requireExit1(t, err)
	assert.Contains(t, out, `"result": "anonymous"`)
}

func TestAuthStatusAllMultipleDefaultsExit1(t *testing.T) {
	a, b := authServer(t, 200), authServer(t, 200)
	writeAuthConfig(t, authHost{key: a, isDefault: true}, authHost{key: b, isDefault: true})
	out, _, err := runAuthStatus(t, "--all")
	requireExit1(t, err)
	assert.Contains(t, out, "multiple defaults, also "+b)
}

func TestAuthStatusHostWithoutDefault(t *testing.T) {
	a, b := authServer(t, 200), authServer(t, 200)
	writeAuthConfig(t, authHost{key: a}, authHost{key: b})
	out, _, err := runAuthStatus(t, "--host", b)
	require.NoError(t, err)
	assert.Contains(t, out, "Host:  "+b+"\n")
}

func TestAuthStatusAllRejectedCredentialsExit1(t *testing.T) {
	for _, tc := range []struct {
		code  int
		label string
	}{
		{401, "invalid token (401)"},
		{403, "forbidden (403)"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			writeAuthConfig(t, authHost{key: authServer(t, 200), isDefault: true}, authHost{key: authServer(t, tc.code)})
			out, _, err := runAuthStatus(t, "--all")
			requireExit1(t, err)
			assert.Contains(t, out, tc.label)
		})
	}
}

func TestAuthStatusAllUnverifiedWarnsExit0(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	writeAuthConfig(t,
		authHost{key: authServer(t, 200), isDefault: true},
		authHost{key: authServer(t, 500)},
		authHost{key: closedURL},
	)

	out, stderr, err := runAuthStatus(t, "--all")
	require.NoError(t, err)
	assert.Contains(t, out, "HTTP 500")
	assert.Contains(t, out, "unreachable")
	assert.Contains(t, stderr, "warning: 2 host(s) could not be checked")
}

func TestAuthStatusAllKeyProblemsExit1(t *testing.T) {
	ok := authServer(t, 200)
	other := authServer(t, 200)
	t.Run("malformed key", func(t *testing.T) {
		writeAuthConfig(t, authHost{key: other + "/#frag", isDefault: true})
		out, _, err := runAuthStatus(t, "--all")
		requireExit1(t, err)
		assert.Contains(t, out, "not normalised: "+other)
	})
	t.Run("duplicate key", func(t *testing.T) {
		writeAuthConfig(t, authHost{key: ok, isDefault: true}, authHost{key: ok + "/"})
		out, _, err := runAuthStatus(t, "--all")
		requireExit1(t, err)
		assert.Contains(t, out, "duplicate of "+ok+"/")
		assert.Contains(t, out, "duplicate of "+ok)
	})
	t.Run("duplicate alias", func(t *testing.T) {
		writeAuthConfig(t, authHost{key: ok, alias: "x", isDefault: true}, authHost{key: other, alias: "x"})
		out, _, err := runAuthStatus(t, "--all")
		requireExit1(t, err)
		assert.Contains(t, out, "alias 'x' shared with "+other)
		assert.Contains(t, out, "alias 'x' shared with "+ok)
	})
}

func TestAuthStatusAllJSON(t *testing.T) {
	a, b := authServer(t, 200), authServer(t, 401)
	writeAuthConfig(t, authHost{key: b, alias: "x"}, authHost{key: a, alias: "x", isDefault: true})

	out, _, err := runAuthStatus(t, "--all", "--json")
	requireExit1(t, err)
	again, _, _ := runAuthStatus(t, "--all", "--json")
	assert.Equal(t, out, again, "JSON output must be stable across runs")

	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	want := []map[string]any{
		{"host": a, "alias": "x", "user": "admin", "default": true, "result": "valid", "status_code": float64(200),
			"problems": []any{"alias 'x' shared with " + b}},
		{"host": b, "alias": "x", "user": "admin", "default": false, "result": "invalid", "status_code": float64(401),
			"problems": []any{"alias 'x' shared with " + a}},
	}
	if a > b {
		want[0], want[1] = want[1], want[0]
	}
	assert.Equal(t, want, got)
}

func TestAuthStatusAllJSONEmptyProblems(t *testing.T) {
	writeAuthConfig(t, authHost{key: authServer(t, 200), isDefault: true})
	out, _, err := runAuthStatus(t, "--all", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"problems": []`)
}

func TestAuthStatusAllFormat(t *testing.T) {
	a := authServer(t, 200)
	writeAuthConfig(t, authHost{key: a, isDefault: true})
	out, _, err := runAuthStatus(t, "--all", "--format", "{{range .}}{{.Host}}={{.Result}}\n{{end}}")
	require.NoError(t, err)
	assert.Equal(t, a+"=valid\n", out)
}

func TestAuthStatusAllRejectsHost(t *testing.T) {
	a := authServer(t, 200)
	writeAuthConfig(t, authHost{key: a, isDefault: true})
	_, _, err := runAuthStatus(t, "--all", "--host", a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all and --host")
}

func TestAuthStatusHostNormalized(t *testing.T) {
	a, b := authServer(t, 200), authServer(t, 200)
	writeAuthConfig(t, authHost{key: a, isDefault: true}, authHost{key: b})

	out, _, err := runAuthStatus(t, "--host", b+"/")
	require.NoError(t, err)
	assert.Contains(t, out, "Host:  "+b+"\n")
	assert.Contains(t, out, "Auth:  valid")
}

func TestAuthStatusSingleHostNoTokenOnFailure(t *testing.T) {
	writeAuthConfig(t, authHost{key: authServer(t, 401), isDefault: true})
	_, _, err := runAuthStatus(t)
	requireExit1(t, err)
}
