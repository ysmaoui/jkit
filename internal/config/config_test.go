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
	t.Setenv("JK_CONFIG_DIR", dir)
	cfg, err := Load()
	require.NoError(t, err)
	assert.NotNil(t, cfg.Hosts)
	assert.Empty(t, cfg.Hosts)
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JK_CONFIG_DIR", dir)

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
	t.Setenv("JK_CONFIG_DIR", dir)

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

func TestXDGConfigPath(t *testing.T) {
	t.Setenv("JK_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	dir, err := ConfigDir()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/xdg/jk", dir)
}
