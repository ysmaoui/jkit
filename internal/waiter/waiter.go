// Package waiter polls a build, or one stage of it, until it has a result.
package waiter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ErrNoStageData means the build has no stage list at all: not a pipeline, or
// neither Pipeline Graph View nor Blue Ocean answers. No later poll changes
// that, so a stage target fails at once instead of waiting out the build.
var ErrNoStageData = errors.New("build has no stage data")

// BuildSource is the part of the API client ReadBuild reads. GetJob and
// GetQueuedTaskURLs tell a build still in the queue, which has no number
// yet, from one that will never exist.
type BuildSource interface {
	GetBuild(jobPath string, number int) (*jenkins.Build, error)
	GetJob(jobPath string) (*jenkins.Job, error)
	GetQueuedTaskURLs() ([]string, error)
}

// Source is the part of the API client the poller reads.
type Source interface {
	BuildSource
	// GetPipelineStages returns nil, nil when there is no stage data, and an
	// empty non-nil slice for a pipeline with no stages yet.
	GetPipelineStages(jobPath string, number int) ([]jenkins.Stage, error)
}

// Pending says why ReadBuild found no build.
type Pending int

const (
	NotPending Pending = iota
	// Queued means a queued item of the job can still take the number.
	Queued
	// Starting means the number is assigned but the build is not readable
	// yet: Jenkins assigns it before it registers the build.
	Starting
)

type Target struct {
	JobPath string
	Build   int
	// Stage is a name, qualified path or node ID. Empty waits for the build.
	Stage string
	// UntilListed ends the wait as soon as Stage appears in the stage list,
	// result or not, for a caller that follows the stage from its start.
	UntilListed bool
}

type Result struct {
	// Status is the build result, or the stage status for a stage target.
	Status         string
	DurationMillis int64
	// StagePath is the resolved qualified path, empty if the stage never appeared.
	StagePath string
	// StageID is set only for an UntilListed target.
	StageID string
	// NotRun explains why a stage target has no result: the build finished
	// before the stage produced one. Empty otherwise.
	NotRun string
}

type Poller struct {
	Source   Source
	Interval time.Duration
	// OnRunning runs after each poll that finds the target unfinished. Wait
	// stops waiting for it when ctx ends, so it may make blocking requests.
	OnRunning func()
	// OnQueued runs after each poll that finds the build still in the queue.
	OnQueued func()
	// QueuedInterval, if set, replaces Interval after such a poll. A queued
	// poll costs three requests.
	QueuedInterval time.Duration
}

// Wait polls until the target has a result or ctx ends, in which case it
// returns ctx.Err(). The first poll is immediate, so a finished target
// returns without sleeping.
func (p Poller) Wait(ctx context.Context, t Target) (Result, error) {
	var stageID string
	pending := NotPending
	for first := true; ; first = false {
		if !first {
			interval := p.Interval
			if pending != NotPending && p.QueuedInterval > 0 {
				interval = p.QueuedInterval
			}
			if err := sleep(ctx, interval); err != nil {
				return Result{}, err
			}
		}

		// The build is read before the stages so that a finished build implies
		// the stage list that follows is final too.
		build, state, err := ReadBuild(ctx, p.Source, t.JobPath, t.Build, pending == Starting)
		if err != nil {
			return Result{}, err
		}
		pending = state
		if build == nil {
			if p.OnQueued != nil {
				p.OnQueued()
			}
			continue
		}
		if t.Stage == "" {
			if !build.Building {
				return Result{Status: build.Result, DurationMillis: build.Duration}, nil
			}
		} else {
			res, done, err := p.pollStage(ctx, t, &stageID, build)
			if err != nil || done {
				return res, err
			}
		}

		if p.OnRunning != nil {
			if _, err := call(ctx, func() (struct{}, error) { p.OnRunning(); return struct{}{}, nil }); err != nil {
				return Result{}, err
			}
		}
	}
}

// ReadBuild reads build n, or reports why it is not there yet. Jenkins
// numbers a build only when it leaves the queue, so until then the number a
// trigger expects reads as not found. A number that no queued item can take
// is a real not-found, as is a missing job. A build that stays Starting
// across two calls is gone: pass startingBefore when the last call returned
// Starting.
func ReadBuild(ctx context.Context, src BuildSource, jobPath string, n int, startingBefore bool) (*jenkins.Build, Pending, error) {
	get := func() (*jenkins.Build, error) { return src.GetBuild(jobPath, n) }
	build, err := call(ctx, get)
	var nf *jenkins.NotFoundError
	if !errors.As(err, &nf) {
		return build, NotPending, err
	}

	notFound := err

	// The queue is read before the job, so an item that leaves the queue in
	// between still counts, as the job's raised nextBuildNumber. Without a
	// readable queue the not-found stands.
	queue, err := call(ctx, src.GetQueuedTaskURLs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, NotPending, err
		}
		return nil, NotPending, notFound
	}
	job, err := call(ctx, func() (*jenkins.Job, error) { return src.GetJob(jobPath) })
	var jobNF *jenkins.NotFoundError
	if errors.As(err, &jobNF) {
		return nil, NotPending, &jenkins.NotFoundError{Resource: "job", Name: jobPath, Host: jobNF.Host}
	}
	if err != nil {
		return nil, NotPending, err
	}

	if n < job.NextBuildNumber {
		// Numbered already: it left the queue after the first read, or was
		// deleted. A second read tells which, except for the newest number,
		// which Jenkins assigns a moment before the build becomes readable.
		build, err = call(ctx, get)
		if errors.As(err, &nf) && n == job.NextBuildNumber-1 && !startingBefore {
			return nil, Starting, nil
		}
		return build, NotPending, err
	}
	queued := 0
	for _, u := range queue {
		if u == job.URL {
			queued++
		}
	}
	if n < job.NextBuildNumber+queued {
		return nil, Queued, nil
	}
	return nil, NotPending, fmt.Errorf("build #%d of %s does not exist and no queued build will get that number (next build is #%d, %d queued): %w",
		n, jobPath, job.NextBuildNumber, queued, nf)
}

func (p Poller) pollStage(ctx context.Context, t Target, stageID *string, build *jenkins.Build) (Result, bool, error) {
	stages, err := p.stages(ctx, t)
	if err != nil {
		return Result{}, false, err
	}
	res, done, err := stageResult(stages, t, stageID, build)
	if err != nil || !done || res.NotRun == "" {
		return res, done, err
	}
	// The stage source can trail the build: a stage may still read running
	// just after the build reports finished. Look once more before calling
	// it a stage without a result.
	if err := sleep(ctx, p.Interval); err != nil {
		return Result{}, false, err
	}
	if stages, err = p.stages(ctx, t); err != nil {
		return Result{}, false, err
	}
	return stageResult(stages, t, stageID, build)
}

func (p Poller) stages(ctx context.Context, t Target) ([]jenkins.Stage, error) {
	stages, err := call(ctx, func() ([]jenkins.Stage, error) { return p.Source.GetPipelineStages(t.JobPath, t.Build) })
	if err == nil && stages == nil {
		return nil, ErrNoStageData
	}
	return stages, err
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// stageResult resolves the stage on first sight and pins its ID, so a later
// parallel branch with the same name cannot make the reference ambiguous.
// A stage missing from the list may simply not have started, which only the
// build finishing rules out.
func stageResult(stages []jenkins.Stage, t Target, stageID *string, build *jenkins.Build) (Result, bool, error) {
	if *stageID == "" {
		id, err := jenkins.ResolveStageID(stages, t.Stage)
		var nf *jenkins.StageNotFoundError
		switch {
		case errors.As(err, &nf):
			if build.Building {
				return Result{}, false, nil
			}
			return Result{
				Status: "NOT_BUILT",
				NotRun: fmt.Sprintf("stage %q never ran — build #%d finished %s; stages: %s",
					t.Stage, t.Build, build.Result, strings.Join(nf.Available, ", ")),
			}, true, nil
		case err != nil:
			return Result{}, false, err
		}
		*stageID = id
		if t.UntilListed {
			return Result{StageID: id, StagePath: jenkins.QualifiedStagePaths(stages)[id]}, true, nil
		}
	}

	for _, s := range stages {
		if s.ID != *stageID {
			continue
		}
		res := Result{Status: s.Status, DurationMillis: s.DurationMillis, StagePath: jenkins.QualifiedStagePaths(stages)[s.ID]}
		if HasResult(s.Status) {
			return res, true, nil
		}
		if build.Building {
			return Result{}, false, nil
		}
		status := s.Status
		if status == "" {
			status = "no status"
		}
		res.NotRun = fmt.Sprintf("stage %q has no result (%s) — build #%d finished %s", res.StagePath, status, t.Build, build.Result)
		return res, true, nil
	}
	if build.Building {
		return Result{}, false, nil
	}
	return Result{
		Status: "NOT_BUILT",
		NotRun: fmt.Sprintf("stage %q (id %s) is gone from the stage list — build #%d finished %s", t.Stage, *stageID, t.Build, build.Result),
	}, true, nil
}

// HasResult reports a final stage status. NOT_BUILT is not one while the
// build runs: both sources use it for a stage that has not started yet as well
// as for one skipped by when{}, and Blue Ocean reports UNKNOWN for a running
// stage.
func HasResult(status string) bool {
	switch status {
	case "SUCCESS", "FAILURE", "UNSTABLE", "ABORTED":
		return true
	}
	return false
}

// ExitCode maps a result to the exit code `run --wait` uses for builds.
// Anything without a result, including a stage that never ran, is 4.
func ExitCode(r Result) int {
	if r.NotRun != "" {
		return 4
	}
	switch r.Status {
	case "SUCCESS":
		return 0
	case "FAILURE":
		return 1
	case "UNSTABLE":
		return 2
	case "ABORTED":
		return 3
	}
	return 4
}

// call runs a blocking API read but returns as soon as ctx ends. The client
// takes no context, so without this Ctrl-C or --max-wait would wait out a slow
// request. The abandoned request finishes in the background.
func call[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type out struct {
		v   T
		err error
	}
	ch := make(chan out, 1)
	go func() {
		v, err := f()
		ch <- out{v, err}
	}()
	select {
	case o := <-ch:
		return o.v, o.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
