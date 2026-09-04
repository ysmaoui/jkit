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

func TestStripControl(t *testing.T) {
	tests := map[string]struct{ in, want string }{
		"erase line":     {"ok\x1b[2Khidden rewrite", "okhidden rewrite"},
		"colour":         {"\x1b[31mred\x1b[0m", "red"},
		"osc title":      {"a\x1b]0;pwned\x07b", "ab"},
		"bare escape":    {"a\x1bb", "ab"},
		"carriage retun": {"a\rb", "ab"},
		"tab kept":       {"a\tb", "a\tb"},
		"delete":         {"a\x7fb", "ab"},
		"plain":          {"bumped the timeout", "bumped the timeout"},
		"unicode kept":   {"変更 ✓", "変更 ✓"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripControl(tt.in))
		})
	}
}
