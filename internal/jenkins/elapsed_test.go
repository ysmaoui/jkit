package jenkins

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBuildElapsed(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	cases := []struct {
		name string
		b    Build
		want time.Duration
	}{
		{"running uses timestamp", Build{Building: true, Timestamp: now.UnixMilli() - 90_000}, 90 * time.Second},
		{"running without timestamp", Build{Building: true}, 0},
		{"running with future timestamp", Build{Building: true, Timestamp: now.UnixMilli() + 5000, Duration: 7}, 7 * time.Millisecond},
		{"finished keeps duration", Build{Duration: 30_000, Timestamp: now.UnixMilli() - 999_000}, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.b.Elapsed(now))
		})
	}
}

func TestStageElapsed(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	cases := []struct {
		name string
		s    Stage
		want time.Duration
	}{
		{"in progress uses start", Stage{Status: "IN_PROGRESS", StartTimeMillis: now.UnixMilli() - 125_000}, 125 * time.Second},
		{"in progress without start", Stage{Status: "IN_PROGRESS"}, 0},
		{"paused uses start", Stage{Status: "PAUSED_PENDING_INPUT", StartTimeMillis: now.UnixMilli() - 60_000}, 60 * time.Second},
		{"paused without start", Stage{Status: "PAUSED_PENDING_INPUT"}, 0},
		{"queued uses start", Stage{Status: "QUEUED", StartTimeMillis: now.UnixMilli() - 1_200_000}, 20 * time.Minute},
		{"queued without start", Stage{Status: "QUEUED"}, 0},
		{"finished keeps duration", Stage{Status: "SUCCESS", DurationMillis: 5000, StartTimeMillis: now.UnixMilli() - 999_000}, 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.s.Elapsed(now))
		})
	}
}
