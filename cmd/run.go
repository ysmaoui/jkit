package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/jenkins"
	"github.com/ysmaoui/jk/internal/output"
)

var runCmd = &cobra.Command{
	Use:   "run [job]",
	Short: "Trigger a build",
	Example: `  jk run my-app
  jk run my-app -p BRANCH=main -p ENV=staging
  jk run my-app --wait --log`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRun,
}

func init() {
	runCmd.Flags().StringArrayP("param", "p", nil, "Build parameter (KEY=VALUE)")
	runCmd.Flags().Bool("wait", false, "Wait for build to complete")
	runCmd.Flags().Bool("log", false, "Stream build log (implies --wait)")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	client, jobPath, _, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	// Parse params
	rawParams, _ := cmd.Flags().GetStringArray("param")
	params := make(map[string]string)
	for _, p := range rawParams {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid parameter format %q — use KEY=VALUE", p)
		}
		params[parts[0]] = parts[1]
	}

	wait, _ := cmd.Flags().GetBool("wait")
	showLog, _ := cmd.Flags().GetBool("log")
	if showLog {
		wait = true
	}

	// Trigger
	queueID, err := client.TriggerBuild(jobPath, params)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "Build queued (queue item #%d)\n", queueID)

	if !wait {
		return nil
	}

	// Set up signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Poll queue for build number
	var buildNum int
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
pollQueue:
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("interrupted")
			case <-deadline:
				return fmt.Errorf("queue timeout after 5m — check Jenkins")
			case <-ticker.C:
			}
		}

		item, err := client.GetQueueItem(queueID)
		if err != nil {
			return fmt.Errorf("polling queue: %w", err)
		}
		if item.Executable != nil {
			buildNum = item.Executable.Number
			break pollQueue
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "Build #%d started\n", buildNum)

	// Stream log if requested
	if showLog {
		streamer := output.NewLogStreamer(newFetchLog(client), jobPath, buildNum, os.Stdout)
		if err := streamer.Stream(ctx); err != nil && ctx.Err() == nil {
			return err
		}
	}

	// Fetch final build result (single call if log was streamed, poll loop otherwise)
	buildDeadline := time.After(2 * time.Hour)
	firstPoll := true
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted")
		case <-buildDeadline:
			return fmt.Errorf("build timeout after 2h — check Jenkins for build #%d", buildNum)
		default:
			if !firstPoll {
				select {
				case <-ctx.Done():
					return fmt.Errorf("interrupted")
				case <-buildDeadline:
					return fmt.Errorf("build timeout after 2h — check Jenkins for build #%d", buildNum)
				case <-time.After(2 * time.Second):
				}
			}
			firstPoll = false
		}

		build, err := client.GetBuild(jobPath, buildNum)
		if err != nil {
			return fmt.Errorf("polling build: %w", err)
		}
		if !build.Building {
			d := time.Duration(build.Duration) * time.Millisecond
			_, _ = fmt.Fprintf(os.Stderr, "Build #%d completed: %s (%s)\n", buildNum, build.Result, formatDuration(d))
			switch build.Result {
			case "SUCCESS":
				return nil
			case "FAILURE":
				return &jenkins.ExitError{Code: 1, Message: build.Result}
			case "UNSTABLE":
				return &jenkins.ExitError{Code: 2, Message: build.Result}
			case "ABORTED":
				return &jenkins.ExitError{Code: 3, Message: build.Result}
			default:
				return &jenkins.ExitError{Code: 4, Message: fmt.Sprintf("unknown result: %s", build.Result)}
			}
		}
	}
}
