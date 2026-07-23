package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestMatchJobsCaseInsensitive(t *testing.T) {
	jobs := []jenkins.Job{
		{Name: "api", FullName: "team/backend/api"},
		{Name: "web", FullName: "team/frontend/web"},
		{Name: "API-Gateway", FullName: "team/backend/API-Gateway"},
		{Name: "legacy"}, // no FullName -> falls back to Name
	}

	got := matchJobs(jobs, "api")
	names := make([]string, len(got))
	for i, j := range got {
		names[i] = j.FullName
	}
	assert.Equal(t, []string{"team/backend/api", "team/backend/API-Gateway"}, names)

	// Falls back to Name when FullName is empty.
	assert.Len(t, matchJobs(jobs, "legacy"), 1)

	// No match.
	assert.Empty(t, matchJobs(jobs, "nonexistent"))
}
