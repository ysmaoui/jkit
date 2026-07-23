package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func TestComputeHistoryStats(t *testing.T) {
	// Newest-first: latest completed build has duration 300ms.
	builds := []jenkins.Build{
		{Number: 5, Building: true, Duration: 0},      // in-progress, excluded
		{Number: 4, Result: "SUCCESS", Duration: 300}, // latest completed
		{Number: 3, Result: "FAILURE", Duration: 100}, //
		{Number: 2, Result: "SUCCESS", Duration: 200}, //
		{Number: 1, Result: "SUCCESS", Duration: 0},   // zero duration, excluded from median
	}

	s := computeHistoryStats(builds)
	assert.Equal(t, 4, s.Completed, "in-progress excluded")
	assert.Equal(t, 3, s.Successful)
	// durations {300,100,200} sorted -> median 200
	assert.Equal(t, int64(200), s.MedianMS)
	assert.True(t, s.HasLast)
	assert.Equal(t, int64(300), s.LastMS)
	// (300-200)/200 = +50%
	assert.True(t, s.HasDeltaPct)
	assert.Equal(t, 50, s.DeltaPct)
}

func TestComputeHistoryStatsEvenMedian(t *testing.T) {
	builds := []jenkins.Build{
		{Number: 2, Result: "SUCCESS", Duration: 100},
		{Number: 1, Result: "SUCCESS", Duration: 200},
	}
	s := computeHistoryStats(builds)
	assert.Equal(t, int64(150), s.MedianMS, "even count averages the two middle values")
}

func TestComputeHistoryStatsAllBuilding(t *testing.T) {
	builds := []jenkins.Build{{Number: 1, Building: true}}
	s := computeHistoryStats(builds)
	assert.Equal(t, 0, s.Completed)
	assert.False(t, s.HasLast)
	assert.False(t, s.HasDeltaPct)
}
