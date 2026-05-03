package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/jenkins"
	"github.com/ysmaoui/jk/internal/output"
)

var rebuildCmd = &cobra.Command{
	Use:   "rebuild [job] [build#]",
	Short: "Retrigger a build with the same parameters",
	Example: `  jk rebuild my-app 42
  jk rebuild my-app 42 --wait
  jk rebuild my-app 42 --log`,
	Args: cobra.MaximumNArgs(2),
	RunE: runRebuild,
}

func init() {
	rebuildCmd.Flags().Bool("wait", false, "Wait for build to complete")
	rebuildCmd.Flags().Bool("log", false, "Stream build log (implies --wait)")
	rootCmd.AddCommand(rebuildCmd)
}

func runRebuild(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	// Default to latest build
	if buildNum == 0 {
		builds, err := client.GetBuilds(jobPath, 1)
		if err != nil {
			return err
		}
		if len(builds) == 0 {
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = builds[0].Number
	}

	// Get source build parameters
	build, err := client.GetBuild(jobPath, buildNum)
	if err != nil {
		return err
	}

	params := make(map[string]string)
	for _, p := range build.Parameters() {
		params[p.Name] = p.Value()
	}

	// Trigger new build
	queueID, err := client.TriggerBuild(jobPath, params)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Rebuild queued from #%d (queue item #%d)\n", buildNum, queueID)

	wait, _ := cmd.Flags().GetBool("wait")
	showLog, _ := cmd.Flags().GetBool("log")
	if showLog {
		wait = true
	}
	if !wait {
		return nil
	}

	// Set up signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Poll queue for build number
	var newBuildNum int
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
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
			newBuildNum = item.Executable.Number
			break
		}
	}
	fmt.Fprintf(os.Stderr, "Build #%d started\n", newBuildNum)

	// Stream log if requested
	if showLog {
		streamer := output.NewLogStreamer(newFetchLog(client), jobPath, newBuildNum, os.Stdout)
		if err := streamer.Stream(ctx); err != nil && ctx.Err() == nil {
			return err
		}
	}

	// Poll for result
	buildDeadline := time.After(2 * time.Hour)
	firstPoll := true
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted")
		case <-buildDeadline:
			return fmt.Errorf("build timeout after 2h — check Jenkins for build #%d", newBuildNum)
		default:
			if !firstPoll {
				select {
				case <-ctx.Done():
					return fmt.Errorf("interrupted")
				case <-buildDeadline:
					return fmt.Errorf("build timeout after 2h — check Jenkins for build #%d", newBuildNum)
				case <-time.After(2 * time.Second):
				}
			}
			firstPoll = false
		}

		result, err := client.GetBuild(jobPath, newBuildNum)
		if err != nil {
			return fmt.Errorf("polling build: %w", err)
		}
		if !result.Building {
			d := time.Duration(result.Duration) * time.Millisecond
			fmt.Fprintf(os.Stderr, "Build #%d completed: %s (%s)\n", newBuildNum, result.Result, formatDuration(d))
			switch result.Result {
			case "SUCCESS":
				return nil
			case "FAILURE":
				return &jenkins.ExitError{Code: 1, Message: result.Result}
			case "UNSTABLE":
				return &jenkins.ExitError{Code: 2, Message: result.Result}
			case "ABORTED":
				return &jenkins.ExitError{Code: 3, Message: result.Result}
			default:
				return &jenkins.ExitError{Code: 4, Message: fmt.Sprintf("unknown result: %s", result.Result)}
			}
		}
	}
}
