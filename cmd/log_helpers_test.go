package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
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

func TestStampedWindowMapsTailAndHead(t *testing.T) {
	tests := []struct {
		name       string
		tail, head int
		wantStart  int
		wantEnd    int
	}{
		{"neither", 0, 0, 0, 0},
		{"tail only", 5, 0, -5, 0},
		{"head only", 0, 20, 1, 20},
		{"tail wins the window, head trims after", 5, 2, -5, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := stampedWindow(tc.tail, tc.head)
			assert.Equal(t, tc.wantStart, start)
			assert.Equal(t, tc.wantEnd, end)
		})
	}
}

func TestResolveStageIDByNumericID(t *testing.T) {
	stages := []jenkins.Stage{{ID: "3", Name: "Build"}, {ID: "3366", Name: "Test"}}
	id, err := resolveStageID(stages, "3366")
	require.NoError(t, err)
	assert.Equal(t, "3366", id)
}

func TestResolveStageIDNameBeatsID(t *testing.T) {
	stages := []jenkins.Stage{{ID: "3366", Name: "Build"}, {ID: "7", Name: "3366"}}
	id, err := resolveStageID(stages, "3366")
	require.NoError(t, err)
	assert.Equal(t, "7", id)
}

func TestResolveStageIDNotFoundListsIDs(t *testing.T) {
	stages := []jenkins.Stage{{ID: "3", Name: "Build"}}
	_, err := resolveStageID(stages, "99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Build (3)")
	assert.Contains(t, err.Error(), "--stage-id")
}
