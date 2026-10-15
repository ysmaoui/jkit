package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEmptyDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	cfg, err := Load()
	require.NoError(t, err)
	assert.NotNil(t, cfg.Hosts)
	assert.Empty(t, cfg.Hosts)
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)

	cfg := &Config{
		Hosts: map[string]*HostConfig{
			"https://ci.example.com": {User: "admin", Token: "secret", Default: true},
		},
	}
	require.NoError(t, cfg.Save())

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "admin", loaded.Hosts["https://ci.example.com"].User)
	assert.Equal(t, "secret", loaded.Hosts["https://ci.example.com"].Token)
	assert.True(t, loaded.Hosts["https://ci.example.com"].Default)
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)

	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://ci.example.com": {User: "u", Token: "t"},
	}}
	require.NoError(t, cfg.Save())

	info, err := os.Stat(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestDefaultHostSingle(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://ci.example.com": {User: "u", Token: "t"},
	}}
	host, hc, err := cfg.DefaultHost()
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com", host)
	assert.Equal(t, "u", hc.User)
}

func TestDefaultHostNone(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{}}
	_, _, err := cfg.DefaultHost()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no hosts configured")
}

func TestDefaultHostMultipleNoDefault(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://a.com": {User: "u1", Token: "t1"},
		"https://b.com": {User: "u2", Token: "t2"},
	}}
	_, _, err := cfg.DefaultHost()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "none marked default")
}

func TestDefaultHostMultipleWithDefault(t *testing.T) {
	cfg := &Config{Hosts: map[string]*HostConfig{
		"https://a.com": {User: "u1", Token: "t1"},
		"https://b.com": {User: "u2", Token: "t2", Default: true},
	}}
	host, _, err := cfg.DefaultHost()
	require.NoError(t, err)
	assert.Equal(t, "https://b.com", host)
}

func TestResolveHost(t *testing.T) {
	cfg := &Config{
		Hosts: map[string]*HostConfig{
			"https://jenkins.prod.com":    {User: "admin", Token: "tok1", Alias: "prod"},
			"https://jenkins.staging.com": {User: "admin", Token: "tok2", Alias: "staging"},
		},
	}

	// Exact URL match
	host, hc, err := cfg.ResolveHost("https://jenkins.prod.com")
	require.NoError(t, err)
	assert.Equal(t, "https://jenkins.prod.com", host)
	assert.Equal(t, "tok1", hc.Token)

	// Alias match
	host, hc, err = cfg.ResolveHost("prod")
	require.NoError(t, err)
	assert.Equal(t, "https://jenkins.prod.com", host)
	assert.Equal(t, "tok1", hc.Token)

	// Not found
	_, _, err = cfg.ResolveHost("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.example.com":          "https://x.example.com",
		"https://x.example.com/":         "https://x.example.com",
		"https://x.example.com#":         "https://x.example.com",
		"https://x.example.com/ci/#frag": "https://x.example.com/ci",
		" https://x.example.com/?a=b#c ": "https://x.example.com",
	} {
		assert.Equal(t, want, NormalizeHost(in), in)
	}
}

func TestHostURLsSorted(t *testing.T) {
	c := &Config{Hosts: map[string]*HostConfig{"https://b": {}, "https://a#": {}, "https://a": {}}}
	assert.Equal(t, []string{"https://a", "https://a#", "https://b"}, c.HostURLs())
}

func TestXDGConfigPath(t *testing.T) {
	t.Setenv("JKIT_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	dir, err := ConfigDir()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/xdg/jkit", dir)
}

func TestReplaceWithBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yml")
	orig := []byte("hosts:\n  https://ci.example.com:\n    user: u\n    token: t\n")
	require.NoError(t, os.WriteFile(path, orig, 0600))
	require.NoError(t, os.WriteFile(path+".bak", []byte("old"), 0644))

	cfg := &Config{Hosts: map[string]*HostConfig{"https://qa.example.com": {User: "u", Token: "t"}}}
	backup, err := cfg.ReplaceWithBackup(orig)
	require.NoError(t, err)
	assert.Equal(t, path+".bak", backup)

	got, err := os.ReadFile(backup)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
	loaded, err := Load()
	require.NoError(t, err)
	assert.Contains(t, loaded.Hosts, "https://qa.example.com")
	for _, p := range []string{path, backup} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), p)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temp files left behind")
}

func TestReplaceWithBackupRefusesChangedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("hosts: {}\n"), 0600))

	_, err := (&Config{Hosts: map[string]*HostConfig{}}).ReplaceWithBackup([]byte("other"))
	require.Error(t, err)
	got, _ := os.ReadFile(path)
	assert.Equal(t, "hosts: {}\n", string(got))
	assert.NoFileExists(t, path+".bak")
}

func TestReplaceWithBackupKeepsSymlink(t *testing.T) {
	dir, realDir := t.TempDir(), t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yml")
	target := filepath.Join(realDir, "jkit.yml")
	orig := []byte("hosts: {}\n")
	require.NoError(t, os.WriteFile(target, orig, 0644))
	require.NoError(t, os.Symlink(target, path))

	cfg := &Config{Hosts: map[string]*HostConfig{"https://ci.example.com": {User: "u", Token: "t"}}}
	backup, err := cfg.ReplaceWithBackup(orig)
	require.NoError(t, err)

	link, err := os.Readlink(path)
	require.NoError(t, err)
	assert.Equal(t, target, link)
	loaded, err := Load()
	require.NoError(t, err)
	assert.Contains(t, loaded.Hosts, "https://ci.example.com")
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	assert.Equal(t, path+".bak", backup)
	got, err := os.ReadFile(backup)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
	entries, err := os.ReadDir(realDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left next to the target")
}

func TestReplaceWithBackupErrorsHideContent(t *testing.T) {
	const secret = "tok-5e1f-never-print"
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("hosts:\n  https://ci.example.com:\n    token: "+secret+"\n"), 0600))
	cfg := &Config{Hosts: map[string]*HostConfig{"https://ci.example.com": {User: "u", Token: secret}}}

	_, err := cfg.ReplaceWithBackup([]byte("token: " + secret))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)

	require.NoError(t, os.Mkdir(path+".bak", 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path+".bak", "x"), nil, 0600))
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = cfg.ReplaceWithBackup(current)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
}
