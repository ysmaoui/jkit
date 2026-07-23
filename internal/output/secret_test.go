package output

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSecretKey(t *testing.T) {
	secret := []string{"PASSWORD", "DB_PASSWORD", "API_TOKEN", "githubToken", "AWS_SECRET_ACCESS_KEY", "MY_API_KEY", "CREDENTIAL_ID", "auth_header"}
	for _, k := range secret {
		assert.True(t, IsSecretKey(k), "expected %q to be secret", k)
	}
	plain := []string{"BUILD_NUMBER", "GIT_BRANCH", "PATH", "JOB_NAME", "WORKSPACE"}
	for _, k := range plain {
		assert.False(t, IsSecretKey(k), "expected %q not to be secret", k)
	}
}

func TestMaskSecret(t *testing.T) {
	assert.NotContains(t, MaskSecret("hunter2"), "hunter2")
	assert.Equal(t, "", MaskSecret(""), "empty stays empty")
	assert.Equal(t, "  ", MaskSecret("  "), "blank stays blank")
}
