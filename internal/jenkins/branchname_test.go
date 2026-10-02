package jenkins

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBranchJobName(t *testing.T) {
	tests := map[string]string{
		"main":           "main",
		"feature/x#4":    "feature%2Fx%234",
		`50%?[a]\b`:      "50%25%3F%5Ba%5D%5Cb",
		"my branch:v1@x": "my branch:v1@x",
		"":               "%00",
		".":              "%2E",
		"..":             "%2E.",
		"feature%2Fx":    "feature%252Fx",
	}
	for branch, want := range tests {
		got := BranchJobName(branch)
		assert.Equal(t, want, got, branch)
		assert.True(t, IsBranchJobName(got), got)
		assert.Equal(t, branch, DecodeBranchJobName(got), got)
	}
}

func TestDecodeBranchJobNameKeepsUnknownEscapes(t *testing.T) {
	assert.Equal(t, "a%20b/50%", DecodeBranchJobName("a%20b%2F50%"))
}

func TestIsBranchJobNameRejectsRawBranches(t *testing.T) {
	for _, name := range []string{"feature/x", "x#4", "50%", "50%2", "a%20b", "a?b", "[a]", `a\b`} {
		assert.False(t, IsBranchJobName(name), name)
	}
}
