package output

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactURLCredentials(t *testing.T) {
	tests := map[string]struct{ in, want string }{
		"user and password":  {"https://ci:ghp_secret@git.example.com/org/repo.git", "https://***@git.example.com/org/repo.git"},
		"bare token as user": {"https://ghp_secret@git.example.com/org/repo.git", "https://***@git.example.com/org/repo.git"},
		"no userinfo":        {"https://git.example.com/org/repo.git", "https://git.example.com/org/repo.git"},
		"scp style is left":  {"git@git.example.com:org/repo.git", "git@git.example.com:org/repo.git"},
		"empty":              {"", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := RedactURLCredentials(tt.in)
			assert.Equal(t, tt.want, got)
			if tt.in != tt.want {
				assert.NotContains(t, got, "ghp_secret")
				assert.Contains(t, got, "git.example.com/org/repo.git")
			}
		})
	}
}
