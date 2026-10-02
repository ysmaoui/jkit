package waiter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// fakeSource answers poll n with builds[n] and the n-th stage read with
// stages[n], repeating the last entry of each.
type fakeSource struct {
	builds     []jenkins.Build
	stages     [][]jenkins.Stage
	buildReads int
	stageReads int
	block      chan struct{} // if set, GetBuild blocks until it is closed
}

func (f *fakeSource) GetBuild(string, int) (*jenkins.Build, error) {
	if f.block != nil {
		<-f.block
	}
	b := f.builds[min(f.buildReads, len(f.builds)-1)]
	f.buildReads++
	return &b, nil
}

func (f *fakeSource) GetPipelineStages(string, int) ([]jenkins.Stage, error) {
	s := f.stages[min(f.stageReads, len(f.stages)-1)]
	f.stageReads++
	return s, nil
}

func (f *fakeSource) GetJob(string) (*jenkins.Job, error) { return &jenkins.Job{}, nil }

func (f *fakeSource) GetQueuedTaskURLs() ([]string, error) { return nil, nil }

var (
	running  = jenkins.Build{Number: 1, Building: true}
	finished = jenkins.Build{Number: 1, Result: "SUCCESS"}
)

func deploy(status string) []jenkins.Stage {
	return []jenkins.Stage{{ID: "3", Name: "Build", Status: "SUCCESS"}, {ID: "9", Name: "Deploy", Status: status}}
}

func wait(t *testing.T, src *fakeSource, stage string) (Result, error) {
	t.Helper()
	p := Poller{Source: src, Interval: time.Millisecond}
	return p.Wait(context.Background(), Target{JobPath: "app", Build: 1, Stage: stage})
}

// Blue Ocean reports result UNKNOWN for a running stage.
func TestBlueOceanRunningStageIsNotFinal(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{running}, stages: [][]jenkins.Stage{deploy("UNKNOWN"), deploy("UNKNOWN"), deploy("FAILURE")}}

	res, err := wait(t, src, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, "FAILURE", res.Status)
	assert.Equal(t, 1, ExitCode(res))
	assert.Equal(t, 3, src.stageReads)
}

func TestNoStageDataFailsFast(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{running}, stages: [][]jenkins.Stage{nil}}

	_, err := wait(t, src, "Deploy")
	assert.ErrorIs(t, err, ErrNoStageData)
	assert.Equal(t, 1, src.buildReads)
}

func TestNoStagesYetKeepsWaiting(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{running}, stages: [][]jenkins.Stage{{}, deploy("SUCCESS")}}

	res, err := wait(t, src, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", res.Status)
}

// The stage source can still show the stage running right after the build ends.
func TestFinishedBuildRereadsLaggingStage(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{finished}, stages: [][]jenkins.Stage{deploy("IN_PROGRESS"), deploy("SUCCESS")}}

	res, err := wait(t, src, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", res.Status)
	assert.Empty(t, res.NotRun)
	assert.Equal(t, 2, src.stageReads)
}

func TestFinishedBuildStageStillRunningHasNoResult(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{finished}, stages: [][]jenkins.Stage{deploy("IN_PROGRESS")}}

	res, err := wait(t, src, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, 4, ExitCode(res))
	assert.Contains(t, res.NotRun, `stage "Deploy" has no result (IN_PROGRESS)`)
	assert.Equal(t, 2, src.stageReads, "one re-read, then settle")
}

func TestStageGoneFromList(t *testing.T) {
	gone := []jenkins.Stage{{ID: "3", Name: "Build", Status: "SUCCESS"}}
	src := &fakeSource{builds: []jenkins.Build{running, finished}, stages: [][]jenkins.Stage{deploy("IN_PROGRESS"), gone}}

	res, err := wait(t, src, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, 4, ExitCode(res))
	assert.Contains(t, res.NotRun, `stage "Deploy" (id 9) is gone from the stage list`)
}

func TestCancelMidRequestReturnsPromptly(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{running}, block: make(chan struct{})}
	defer close(src.block)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Poller{Source: src, Interval: time.Hour}.Wait(ctx, Target{JobPath: "app", Build: 1})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

func TestCancelDuringOnRunningReturnsPromptly(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	src := &fakeSource{builds: []jenkins.Build{running}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Poller{Source: src, Interval: time.Millisecond, OnRunning: func() { <-block }}.Wait(ctx, Target{JobPath: "app", Build: 1})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

func TestUntilListedReturnsOnceStageAppears(t *testing.T) {
	src := &fakeSource{builds: []jenkins.Build{running}, stages: [][]jenkins.Stage{{}, deploy("IN_PROGRESS")}}
	p := Poller{Source: src, Interval: time.Millisecond}

	res, err := p.Wait(context.Background(), Target{JobPath: "app", Build: 1, Stage: "Deploy", UntilListed: true})
	require.NoError(t, err)
	assert.Equal(t, "9", res.StageID)
	assert.Equal(t, "Deploy", res.StagePath)
	assert.Equal(t, 2, src.stageReads)
}

const jobURL = "http://jenkins/job/app/"

// buildLookup answers the reads ReadBuild makes. found[i] says whether the
// i-th GetBuild read finds the build; the last entry repeats.
type buildLookup struct {
	found      []bool
	next       int
	queued     int
	buildReads int
}

func (b *buildLookup) GetBuild(string, int) (*jenkins.Build, error) {
	ok := b.found[min(b.buildReads, len(b.found)-1)]
	b.buildReads++
	if !ok {
		return nil, &jenkins.NotFoundError{Resource: "resource", Name: "/job/app/1/api/json", Host: "http://jenkins"}
	}
	return &finished, nil
}

func (b *buildLookup) GetJob(string) (*jenkins.Job, error) {
	return &jenkins.Job{URL: jobURL, NextBuildNumber: b.next}, nil
}

func (b *buildLookup) GetQueuedTaskURLs() ([]string, error) {
	urls := []string{"http://jenkins/job/other/"}
	for range b.queued {
		urls = append(urls, jobURL)
	}
	return urls, nil
}

func TestReadBuildQueued(t *testing.T) {
	src := &buildLookup{found: []bool{false}, next: 1, queued: 1}
	b, state, err := ReadBuild(context.Background(), src, "app", 1, false)
	require.NoError(t, err)
	assert.Nil(t, b)
	assert.Equal(t, Queued, state)
}

// The item left the queue between the first build read and the job read.
func TestReadBuildLeftQueueBetweenReads(t *testing.T) {
	src := &buildLookup{found: []bool{false, true}, next: 2}
	b, state, err := ReadBuild(context.Background(), src, "app", 1, false)
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, NotPending, state)
	assert.Equal(t, 2, src.buildReads)
}

// The newest number is assigned before the build is readable.
func TestReadBuildNewestNumberNotReadableYet(t *testing.T) {
	src := &buildLookup{found: []bool{false}, next: 2}
	b, state, err := ReadBuild(context.Background(), src, "app", 1, false)
	require.NoError(t, err)
	assert.Nil(t, b)
	assert.Equal(t, Starting, state)

	_, _, err = ReadBuild(context.Background(), src, "app", 1, true)
	var nf *jenkins.NotFoundError
	assert.True(t, errors.As(err, &nf), "still unreadable on the next poll: gone, got %v", err)
}

func TestReadBuildDeletedOlderBuild(t *testing.T) {
	src := &buildLookup{found: []bool{false}, next: 5}
	_, _, err := ReadBuild(context.Background(), src, "app", 1, false)
	var nf *jenkins.NotFoundError
	require.True(t, errors.As(err, &nf), "got %v", err)
	assert.Equal(t, 2, src.buildReads)
}

func TestReadBuildPastQueue(t *testing.T) {
	src := &buildLookup{found: []bool{false}, next: 1, queued: 1}
	_, _, err := ReadBuild(context.Background(), src, "app", 2, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "next build is #1, 1 queued")
}

// A build Jenkins has numbered but not yet registered is read again next poll.
func TestWaitRetriesStartingBuild(t *testing.T) {
	src := &lookupSource{buildLookup{found: []bool{false, false, true}, next: 2}}
	pending := 0
	p := Poller{Source: src, Interval: time.Millisecond, OnQueued: func() { pending++ }}

	res, err := p.Wait(context.Background(), Target{JobPath: "app", Build: 1})
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", res.Status)
	assert.Equal(t, 1, pending)
}

type lookupSource struct{ buildLookup }

func (*lookupSource) GetPipelineStages(string, int) ([]jenkins.Stage, error) { return nil, nil }
