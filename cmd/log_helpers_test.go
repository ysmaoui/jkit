package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitLogLines(t *testing.T) {
	assert.Nil(t, splitLogLines(""))
	assert.Nil(t, splitLogLines("\n"))
	assert.Equal(t, []string{"a"}, splitLogLines("a\n"))
	assert.Equal(t, []string{"a", "b"}, splitLogLines("a\nb\n"))
	assert.Equal(t, []string{"a", "b"}, splitLogLines("a\nb"), "no trailing newline")
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.0 KB", humanBytes(1024))
	assert.Equal(t, "1.5 KB", humanBytes(1536))
	assert.Equal(t, "10.0 MB", humanBytes(10<<20))
	assert.Equal(t, "1.0 GB", humanBytes(1<<30))
}

func TestMatcher(t *testing.T) {
	m := matcher("Error", false)
	assert.True(t, m("an Error here"))
	assert.False(t, m("an error here"), "case-sensitive by default")

	mi := matcher("Error", true)
	assert.True(t, mi("an error here"), "case-insensitive")
	assert.True(t, mi("an ERROR here"))
}
