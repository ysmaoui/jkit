package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/config"
)

// Tokens name the answer pruneServer gives, so twins on one server can get
// different results.
const (
	tokValid  = "tok-valid-3b9e-never-print"
	tokStale  = "tok-stale-51c0-never-print"
	tokDown   = "tok-down-7d2a-never-print"
	tokErr500 = "tok-500-e41f-never-print"
	tokForbid = "tok-403-0a6b-never-print"
	tokAnon   = "tok-anon-92cd-never-print"
)

var pruneTokens = []string{tokValid, tokStale, tokDown, tokErr500, tokForbid, tokAnon}

func pruneServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, _ := r.BasicAuth()
		switch token {
		case tokValid:
			_, _ = w.Write([]byte(`{"name":"admin","anonymous":false}`))
		case tokStale:
			w.WriteHeader(http.StatusUnauthorized)
		case tokDown:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		case tokErr500:
			w.WriteHeader(http.StatusInternalServerError)
		case tokForbid:
			w.WriteHeader(http.StatusForbidden)
		case tokAnon:
			_, _ = w.Write([]byte(`{"name":"anonymous","anonymous":true}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type pruneKey struct {
	key, token, alias string
	isDefault         bool
}

func writePruneConfig(t *testing.T, keys ...pruneKey) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	var b strings.Builder
	b.WriteString("hosts:\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %q:\n    user: admin\n    token: %s\n", k.key, k.token)
		if k.alias != "" {
			fmt.Fprintf(&b, "    alias: %s\n", k.alias)
		}
		if k.isDefault {
			b.WriteString("    default: true\n")
		}
	}
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0600))
	return path
}

func runAuthPrune(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout, err = executeCmd(t, append([]string{"auth", "prune"}, args...)...)
	})
	text := stdout + stderr
	if err != nil {
		text += err.Error()
	}
	for _, tok := range pruneTokens {
		if strings.Contains(text, tok) {
			t.Fatal("a token appears in command output or error")
		}
	}
	return stdout, stderr, err
}

type fileState struct {
	data  []byte
	mtime time.Time
}

func statConfig(t *testing.T, path string) fileState {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	return fileState{data, info.ModTime()}
}

func assertUntouched(t *testing.T, path string, before fileState) {
	t.Helper()
	after := statConfig(t, path)
	assert.True(t, string(before.data) == string(after.data), "config.yml content changed")
	assert.Equal(t, before.mtime, after.mtime, "config.yml mtime changed")
	assert.NoFileExists(t, path+".bak")
}

func TestAuthPruneDryRunChangesNothing(t *testing.T) {
	a := pruneServer(t)
	path := writePruneConfig(t,
		pruneKey{key: a, token: tokValid},
		pruneKey{key: a + "#", token: tokStale, alias: "prod", isDefault: true},
	)
	before := statConfig(t, path)

	out, stderr, err := runAuthPrune(t)
	require.NoError(t, err)
	assert.Equal(t, "remove "+a+"#  (not normalised; twin "+a+" is valid)\n"+
		"move   default "+a+"# -> "+a+"\n"+
		"move   alias "+a+"# -> "+a+"\n", out)
	assert.Contains(t, stderr, "dry run: re-run with --yes to apply")
	assertUntouched(t, path, before)
}

func TestAuthPruneYesApplies(t *testing.T) {
	a, b := pruneServer(t), pruneServer(t)
	path := writePruneConfig(t,
		pruneKey{key: a, token: tokValid},
		pruneKey{key: a + "/", token: tokStale, alias: "prod", isDefault: true},
		pruneKey{key: b, token: tokValid, alias: "qa"},
		pruneKey{key: b + "?x=1", token: tokValid, alias: "staging"},
	)
	orig, err := os.ReadFile(path)
	require.NoError(t, err)
	origInfo, err := os.Stat(path)
	require.NoError(t, err)

	out, stderr, err := runAuthPrune(t, "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "remove "+a+"/  (not normalised; twin "+a+" is valid)")
	assert.Contains(t, out, "remove "+b+"?x=1  (not normalised; twin "+b+" is valid)")
	assert.Contains(t, out, "note   "+b+"?x=1  (alias 'staging' dropped; "+b+" keeps alias 'qa')")
	assert.Contains(t, stderr, "removed 2 key(s), backup in "+path+".bak")

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, string(orig), string(bak))
	for _, p := range []string{path, path + ".bak"} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), p)
	}
	newInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, os.SameFile(origInfo, newInfo), "config.yml must be replaced by rename, not rewritten in place")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"config.yml", "config.yml.bak"}, names, "no temp files left")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a, b}, cfg.HostURLs())
	assert.True(t, cfg.Hosts[a].Default)
	assert.Equal(t, "prod", cfg.Hosts[a].Alias)
	assert.Equal(t, tokValid, cfg.Hosts[a].Token)
	assert.False(t, cfg.Hosts[b].Default)
	assert.Equal(t, "qa", cfg.Hosts[b].Alias)

	out, stderr, err = runAuthPrune(t, "--yes")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "nothing to remove")
}

func TestAuthPruneRemovesStaleDuplicate(t *testing.T) {
	a := pruneServer(t)
	path := writePruneConfig(t,
		pruneKey{key: a, token: tokValid, isDefault: true},
		pruneKey{key: a + "#", token: tokStale},
	)
	_, _, err := runAuthPrune(t, "--yes")
	require.NoError(t, err)
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{a}, cfg.HostURLs())
	assert.FileExists(t, path+".bak")
}

// A fragment key checks valid against the normalised URL but breaks every API
// call, so the default must not land on it.
func TestAuthPruneSkipsUnusableHeir(t *testing.T) {
	a := pruneServer(t)
	writePruneConfig(t,
		pruneKey{key: a, token: tokStale, alias: "prod", isDefault: true},
		pruneKey{key: a + "/", token: tokValid},
		pruneKey{key: a + "#", token: tokValid, alias: "other"},
	)
	_, _, err := runAuthPrune(t, "--yes")
	require.NoError(t, err)
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a + "/", a + "#"}, cfg.HostURLs())
	assert.True(t, cfg.Hosts[a+"/"].Default)
	assert.Equal(t, "prod", cfg.Hosts[a+"/"].Alias)
	assert.False(t, cfg.Hosts[a+"#"].Default)
}

func TestAuthPruneKeepsOtherUsersKey(t *testing.T) {
	a := pruneServer(t)
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(
		"hosts:\n  %q:\n    user: admin\n    token: %s\n    default: true\n  %q:\n    user: bob\n    token: %s\n",
		a, tokValid, a+"/", tokStale)), 0600))
	before := statConfig(t, path)

	out, _, err := runAuthPrune(t, "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "keep   "+a+"/  (twin "+a+" is valid but not as this key's user")
	assertUntouched(t, path, before)
}

func TestAuthPruneNeverRemoves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keys   func(h string) []pruneKey
		report string
	}{
		{"unreachable twin", twinWith(tokDown), "not normalised; twin %[1]s is unreachable, not proven valid"},
		{"5xx twin", twinWith(tokErr500), "not normalised; twin %[1]s is http_500, not proven valid"},
		{"forbidden twin", twinWith(tokForbid), "not normalised; twin %[1]s is forbidden, not proven valid"},
		{"anonymous twin", twinWith(tokAnon), "not normalised; twin %[1]s is anonymous, not proven valid"},
		{"401 without valid twin", func(h string) []pruneKey {
			return []pruneKey{{key: h, token: tokStale, isDefault: true}, {key: h + "#", token: tokErr500}}
		}, "keep   %[1]s  (invalid token, no valid twin; run: jkit auth login --host %[1]s)"},
		{"only entry for host", func(h string) []pruneKey {
			return []pruneKey{{key: h, token: tokStale, isDefault: true}}
		}, "keep   %[1]s  (invalid token, no valid twin; run: jkit auth login --host %[1]s)"},
		{"malformed key without twin", func(h string) []pruneKey {
			return []pruneKey{{key: h + "/#frag", token: tokValid, isDefault: true}}
		}, "keep   %[1]s/#frag  (not normalised, no twin; run: jkit auth login --host %[1]s, then jkit config remove '%[1]s/#frag')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := pruneServer(t)
			path := writePruneConfig(t, tc.keys(h)...)
			before := statConfig(t, path)

			out, stderr, err := runAuthPrune(t, "--yes")
			require.NoError(t, err)
			assert.NotRegexp(t, `(?m)^remove `, out)
			assert.Contains(t, out, fmt.Sprintf(tc.report, h))
			assert.Contains(t, stderr, "nothing to remove")
			assertUntouched(t, path, before)
		})
	}
}

func twinWith(token string) func(h string) []pruneKey {
	return func(h string) []pruneKey {
		return []pruneKey{{key: h, token: token, isDefault: true}, {key: h + "#", token: tokValid}}
	}
}

func TestAuthPruneJSON(t *testing.T) {
	a := pruneServer(t)
	writePruneConfig(t,
		pruneKey{key: a, token: tokValid},
		pruneKey{key: a + "#", token: tokStale, isDefault: true},
	)
	out, _, err := runAuthPrune(t, "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, map[string]any{
		"dry_run":  true,
		"removals": []any{map[string]any{"host": a + "#", "reason": "not normalised; twin " + a + " is valid"}},
		"reports":  []any{},
		"moves":    []any{map[string]any{"from": a + "#", "to": a, "field": "default"}},
	}, got)

	out, _, err = runAuthPrune(t, "--json", "--yes")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, false, got["dry_run"])
	assert.Equal(t, filepath.Join(os.Getenv("JKIT_CONFIG_DIR"), "config.yml.bak"), got["backup"])
}

func TestAuthPruneFormat(t *testing.T) {
	a := pruneServer(t)
	writePruneConfig(t, pruneKey{key: a, token: tokValid}, pruneKey{key: a + "/", token: tokValid})
	out, _, err := runAuthPrune(t, "--format", "{{.DryRun}}{{range .Removals}} {{.Host}}{{end}}")
	require.NoError(t, err)
	assert.Equal(t, "true "+a+"/", out)
}

func TestAuthPruneUnreadableConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte("hosts: [\n"), 0600))
	_, _, err := runAuthPrune(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}
