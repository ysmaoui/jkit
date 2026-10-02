package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

var rebuildCmd = &cobra.Command{
	Use:   "rebuild [job] [build#]",
	Short: "Retrigger a build with the same parameters",
	Example: `  jkit rebuild my-app 42
  jkit rebuild my-app 42 --wait
  jkit rebuild my-app 42 --log`,
	Args: cobra.MaximumNArgs(2),
	RunE: runRebuild,
}

func init() {
	registerRebuildFlags(rebuildCmd)
	withResultExit(rebuildCmd)
	rootCmd.AddCommand(rebuildCmd)
}

// registerRebuildFlags is shared with the test harness, which resets flags.
func registerRebuildFlags(c *cobra.Command) {
	c.Flags().Bool("wait", false, "Wait for build to complete")
	c.Flags().Bool("log", false, "Stream build log (implies --wait)")
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
	res, err := client.TriggerBuild(jobPath, params)
	if err != nil {
		return err
	}
	if res.Indexing {
		return fmt.Errorf("%s indexes branches and has no builds to rebuild; use 'jkit run %s'", jobPath, jobPath)
	}
	queueID := res.QueueID
	_, _ = fmt.Fprintf(os.Stderr, "Rebuild queued from #%d (queue item #%d)\n", buildNum, queueID)

	wait, _ := cmd.Flags().GetBool("wait")
	showLog, _ := cmd.Flags().GetBool("log")
	if showLog {
		wait = true
	}
	if !wait {
		return nil
	}

	ctx, cancel := interruptContext()
	defer cancel()

	// Poll queue for build number
	var newBuildNum int
	deadline := time.After(queueTimeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return errInterrupted
			case <-deadline:
				return &jenkins.ExitError{Code: 5, Message: fmt.Sprintf("queue timeout after %s — check Jenkins", queueTimeout)}
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
	_, _ = fmt.Fprintf(os.Stderr, "Build #%d started\n", newBuildNum)

	// Stream log if requested
	if showLog {
		if err := streamLog(ctx, client.ConsoleLog(jobPath, newBuildNum), os.Stdout, os.Stderr); err != nil && ctx.Err() == nil {
			return err
		}
	}

	return waitForBuildResult(ctx, client, jobPath, newBuildNum)
}
