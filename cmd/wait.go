package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
	"github.com/ysmaoui/jkit/internal/waiter"
)

// waitPollInterval is a variable so tests drive the loop without real sleeps.
var waitPollInterval = 5 * time.Second

var waitCmd = &cobra.Command{
	Use:   "wait [job] [build#]",
	Short: "Block until a build or stage finishes; exit code is its result",
	Example: `  jkit wait my-app 42
  jkit wait my-app 42 --stage Deploy
  jkit wait my-app --max-wait 30m`,
	Args: cobra.MaximumNArgs(2),
	RunE: runWait,
}

func init() {
	registerWaitFlags(waitCmd)
	withResultExit(waitCmd)
	// Not on run or rebuild: when their flags fail to parse, whether --wait
	// was asked for is unknown.
	waitCmd.SetFlagErrorFunc(resultExit)
	rootCmd.AddCommand(waitCmd)
}

// registerWaitFlags is shared with the test harness, which resets flags.
// The limit is --max-wait because the global --timeout is the HTTP timeout.
func registerWaitFlags(c *cobra.Command) {
	c.Flags().String("stage", "", "Wait for one pipeline stage (name, qualified path like \"Branch/Stage\", or ID from 'jkit stages')")
	c.Flags().Duration("max-wait", 0, "Give up after this long and exit 5 (0 = no limit)")
}

type waitResult struct {
	Job            string `json:"job"`
	Build          int    `json:"build"`
	Stage          string `json:"stage,omitempty"`
	Result         string `json:"result"`
	DurationMillis int64  `json:"durationMillis"`
}

func runWait(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	if buildNum == 0 {
		builds, err := client.GetBuilds(jobPath, 1)
		if err != nil {
			return err
		}
		if len(builds) == 0 {
			if hint := client.ContainerHint(jobPath); hint != nil {
				return hint
			}
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = builds[0].Number
	}

	stage, _ := cmd.Flags().GetString("stage")
	maxWait, _ := cmd.Flags().GetDuration("max-wait")

	ctx, cancel := interruptContext()
	defer cancel()
	if maxWait > 0 {
		ctx, cancel = context.WithTimeout(ctx, maxWait)
		defer cancel()
	}

	what := fmt.Sprintf("build #%d of %s", buildNum, jobPath)
	if stage != "" {
		what = fmt.Sprintf("stage %q in %s", stage, what)
	}
	announced := map[string]bool{}
	nextInputCheck := time.Now().Add(inputPollInterval)
	started, queued := false, false
	p := waiter.Poller{
		Source:   client,
		Interval: waitPollInterval,
		OnQueued: func() {
			if !queued {
				queued = true
				_, _ = fmt.Fprintf(os.Stderr, "Build #%d of %s has not started yet; waiting for it to leave the queue...\n", buildNum, jobPath)
			}
		},
		OnRunning: func() {
			if !started {
				started = true
				_, _ = fmt.Fprintf(os.Stderr, "Waiting for %s...\n", what)
			}
			if time.Now().After(nextInputCheck) {
				nextInputCheck = time.Now().Add(inputPollInterval)
				announcePendingInputs(client, jobPath, buildNum, announced)
			}
		},
	}

	res, err := p.Wait(ctx, waiter.Target{JobPath: jobPath, Build: buildNum, Stage: stage})
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &jenkins.ExitError{Code: 5, Message: fmt.Sprintf("gave up after %s (--max-wait) waiting for %s", maxWait, what)}
		case ctx.Err() != nil:
			return errInterrupted
		case errors.Is(err, waiter.ErrNoStageData):
			return fmt.Errorf("no stages for build #%d of %s — --stage needs a pipeline job and the Pipeline Graph View or Blue Ocean plugin", buildNum, jobPath)
		}
		return withStageHint(err, "the stage ID")
	}

	out := waitResult{Job: jobPath, Build: buildNum, Result: res.Status, DurationMillis: res.DurationMillis}
	if stage != "" {
		out.Stage = res.StagePath
		if out.Stage == "" {
			out.Stage = stage
		}
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		if err := output.NewFormatter(os.Stdout, isJSON, tmpl).Output(out, nil); err != nil {
			return err
		}
	} else {
		subject := fmt.Sprintf("%s #%d", jobPath, buildNum)
		if out.Stage != "" {
			subject += fmt.Sprintf(" stage %q", out.Stage)
		}
		d := time.Duration(res.DurationMillis) * time.Millisecond
		_, _ = fmt.Fprintf(os.Stdout, "%s: %s (%s)\n", subject, output.ColorStatus(res.Status), formatDuration(d))
	}

	code := waiter.ExitCode(res)
	switch {
	case code == 0:
		return nil
	case res.NotRun != "":
		return &jenkins.ExitError{Code: code, Message: res.NotRun}
	case code == 4:
		return &jenkins.ExitError{Code: code, Message: fmt.Sprintf("unknown result: %s", res.Status)}
	}
	// The result line on stdout already names the failure.
	return &jenkins.ExitError{Code: code}
}
