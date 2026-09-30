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

// Source is the part of the API client the poller reads.
type Source interface {
	GetBuild(jobPath string, number int) (*jenkins.Build, error)
	// GetPipelineStages returns nil, nil when there is no stage data, and an
	// empty non-nil slice for a pipeline with no stages yet.
	GetPipelineStages(jobPath string, number int) ([]jenkins.Stage, error)
}

type Target struct {
	JobPath string
	Build   int
	// Stage is a name, qualified path or node ID. Empty waits for the build.
	Stage string
}

type Result struct {
	// Status is the build result, or the stage status for a stage target.
	Status         string
	DurationMillis int64
	// StagePath is the resolved qualified path, empty if the stage never appeared.
	StagePath string
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
}

// Wait polls until the target has a result or ctx ends, in which case it
// returns ctx.Err(). The first poll is immediate, so a finished target
// returns without sleeping.
func (p Poller) Wait(ctx context.Context, t Target) (Result, error) {
	var stageID string
	for first := true; ; first = false {
		if !first {
			if err := p.sleep(ctx); err != nil {
				return Result{}, err
			}
		}

		// The build is read before the stages so that a finished build implies
		// the stage list that follows is final too.
		build, err := call(ctx, func() (*jenkins.Build, error) { return p.Source.GetBuild(t.JobPath, t.Build) })
		if err != nil {
			return Result{}, err
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
	if err := p.sleep(ctx); err != nil {
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

func (p Poller) sleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.Interval):
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
