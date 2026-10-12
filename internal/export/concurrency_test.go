package export

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedRun has a log for every step, and the early ones answer slowest so
// concurrent reads finish out of step order.
func scriptedRun(t *testing.T) *fakeRun {
	t.Helper()
	run := loadRun(t, "done", "scripted")
	ids := scriptedIDs
	for i, id := range ids {
		run.stepLogs[id] = strings.Repeat(fmt.Sprintf("out of step %s\n", id), 100*(i+1))
		run.delay[id] = time.Duration(len(ids)-i) * 5 * time.Millisecond
	}
	run.exceptions["21"] = "script returned exit code 3"
	run.failLog["24"] = http.StatusInternalServerError
	run.console = "Started by user admin\n"
	return run
}

func TestCollectConcurrentMatchesSequential(t *testing.T) {
	run := scriptedRun(t)
	client := run.serve(t)
	page := func(concurrency int) []byte {
		snap, err := collect(t, client, run.job, Options{Concurrency: concurrency})
		require.NoError(t, err)
		snap.ExportedAtMillis = 0
		var buf bytes.Buffer
		require.NoError(t, Render(&buf, snap))
		return buf.Bytes()
	}
	want := page(1)
	for _, c := range []int{2, 4, 8} {
		assert.Equal(t, want, page(c), "concurrency %d", c)
	}
}

func TestCollectConcurrentProgressInStepOrder(t *testing.T) {
	run := scriptedRun(t)
	var got, want []string
	var seen []int
	snap, err := collect(t, run.serve(t), run.job, Options{Concurrency: 4, LogSource: LogSourceSteps, Progress: func(i, total int, n *Node, s *Step) {
		if s == nil {
			got = append(got, "console")
			return
		}
		assert.Equal(t, 11, total)
		seen = append(seen, i)
		got = append(got, s.ID)
	}})
	require.NoError(t, err)
	snap.EachStep(func(_ *Node, s *Step) { want = append(want, s.ID) })
	assert.Equal(t, append(want, "console"), got)
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, seen)
}

func TestCollectConcurrentRecordsStepError(t *testing.T) {
	run := scriptedRun(t)
	snap, err := collect(t, run.serve(t), run.job, Options{Concurrency: 4})
	require.NoError(t, err)
	failed := find(snap.Stages, "18").Steps[1]
	assert.Equal(t, "24", failed.ID)
	assert.Contains(t, failed.Error, "500")
	assert.True(t, failed.Log.Partial)
	assert.Equal(t, "script returned exit code 3", find(snap.Stages, "18").Steps[0].Exception)
	assert.Contains(t, decode(t, find(snap.Stages, "52").Steps[0].Log), "out of step 53\n", "later steps still read")
}

// openSpools counts this process's open files in dir, unlinked or not: on
// Linux the spool is unlinked at creation, so the directory listing alone
// cannot show a leak.
func openSpools(t *testing.T, dir string) int {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("needs /proc")
	}
	n := 0
	for _, fd := range fds {
		target, err := os.Readlink("/proc/self/fd/" + fd.Name())
		if err == nil && strings.HasPrefix(target, dir+"/") {
			n++
		}
	}
	return n
}

func TestCollectConcurrentStopsOnFatalError(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	run.stall["7"] = true
	run.failLog["31"] = http.StatusUnauthorized
	dir := t.TempDir()

	start := time.Now()
	snap, err := Collect(context.Background(), run.serve(t), run.job, 1, Options{Concurrency: 4, SpoolDir: dir})
	require.Error(t, err)
	assert.Nil(t, snap)
	assert.Contains(t, err.Error(), "auth login")
	assert.Less(t, time.Since(start), 3*time.Second, "the stalled read is cancelled")
	assert.Zero(t, openSpools(t, dir))
}

func TestCollectConcurrentStopsOnCancel(t *testing.T) {
	run := loadRun(t, "done", "declarative")
	for _, id := range []string{"7", "14", "31", "32", "40", "51", "56"} {
		run.stall[id] = true
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last int
	progress := func(i, total int, n *Node, s *Step) {
		last = i
		if i == 2 {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
	}

	start := time.Now()
	snap, err := Collect(ctx, run.serve(t), run.job, 1, Options{Concurrency: 2, SpoolDir: dir, Progress: progress})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, snap)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Equal(t, 2, last, "both workers stall, so no third step is handed out")
	assert.Zero(t, openSpools(t, dir))
}

func TestCollectConcurrentCloseRemovesEverySpool(t *testing.T) {
	run := scriptedRun(t)
	dir := t.TempDir()
	snap, err := Collect(context.Background(), run.serve(t), run.job, 1, Options{Concurrency: 4, SpoolDir: dir, LogSource: LogSourceSteps})
	require.NoError(t, err)
	assert.Equal(t, 4, openSpools(t, dir))
	require.NoError(t, snap.Close())
	assert.Zero(t, openSpools(t, dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
