package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncate(t *testing.T) {
	// Under the limit: unchanged.
	assert.Equal(t, "short", truncate("short", 60))

	// At the limit: unchanged.
	assert.Equal(t, "abcde", truncate("abcde", 5))

	// Over the limit: cut to max runes, last rune is the ellipsis.
	got := truncate("abcdefghij", 5)
	assert.Equal(t, "abcd…", got)
	assert.Equal(t, 5, len([]rune(got)))

	// Rune-safe: multibyte characters are not split.
	got = truncate("héllo wörld", 6)
	assert.Equal(t, "héllo…", got)
	assert.Equal(t, 6, len([]rune(got)))

	// Degenerate widths.
	assert.Equal(t, "…", truncate("abc", 1))
}
