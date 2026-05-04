package context

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveFromJKYml(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".jkit.yml"), []byte("job: team/my-service\nhost: https://ci.example.com\n"), 0644))

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(oldWd) }()

	ctx, err := resolveFromJKYml()
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "team/my-service", ctx.JobPath)
	assert.Equal(t, "https://ci.example.com", ctx.Host)
}

func TestResolveFromJKYmlNotFound(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(oldWd) }()

	ctx, err := resolveFromJKYml()
	assert.Error(t, err)
	assert.Nil(t, ctx)
}

func TestResolveFromDirname(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "my-project")
	require.NoError(t, os.MkdirAll(sub, 0755))

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(sub))
	defer func() { _ = os.Chdir(oldWd) }()

	ctx, err := resolveFromDirname()
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "my-project", ctx.JobPath)
}

func TestResolveChainError(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(oldWd) }()

	// No .jkit.yml, no git, dirname will work as fallback
	ctx, err := Resolve()
	// Should succeed via dirname fallback
	require.NoError(t, err)
	assert.NotEmpty(t, ctx.JobPath)
}

func TestParseGitRemoteSSH(t *testing.T) {
	got, err := ParseGitRemote("git@github.com:org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", got)
}

func TestParseGitRemoteHTTPS(t *testing.T) {
	got, err := ParseGitRemote("https://github.com/org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", got)
}

func TestParseGitRemoteSSHProtocol(t *testing.T) {
	got, err := ParseGitRemote("ssh://git@bitbucket:7999/org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", got)
}

func TestParseGitRemoteHTTPSWithAuth(t *testing.T) {
	got, err := ParseGitRemote("https://user:pass@github.com/org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", got)
}

func TestParseGitRemoteNoSuffix(t *testing.T) {
	got, err := ParseGitRemote("https://github.com/org/repo")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", got)
}

func TestParseGitRemoteInvalid(t *testing.T) {
	_, err := ParseGitRemote("not-a-url")
	assert.Error(t, err)
}
