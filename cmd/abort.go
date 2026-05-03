package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/output"
)

var abortCmd = &cobra.Command{
	Use:   "abort [job] [build#]",
	Short: "Abort a running build",
	Example: `  jk abort my-app
  jk abort my-app 42
  jk abort my-app 42 --wait`,
	Args: cobra.MaximumNArgs(2),
	RunE: runAbort,
}

func init() {
	abortCmd.Flags().BoolP("wait", "w", false, "Wait until the build actually stops")
	rootCmd.AddCommand(abortCmd)
}

func runAbort(cmd *cobra.Command, args []string) error {
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

	// Check if build is running
	build, err := client.GetBuild(jobPath, buildNum)
	if err != nil {
		return err
	}
	if !build.Building {
		fmt.Fprintf(os.Stderr, "Build #%d is not running (result: %s)\n", buildNum, build.Result)
		return nil
	}

	if err := client.StopBuild(jobPath, buildNum); err != nil {
		return err
	}

	wait, _ := cmd.Flags().GetBool("wait")
	if !wait {
		fmt.Fprintf(os.Stderr, "Build #%d abort signal sent\n", buildNum)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Waiting for build #%d to stop...\n", buildNum)
	start := time.Now()
	timeout := 2 * time.Minute
	for {
		time.Sleep(2 * time.Second)
		b, err := client.GetBuild(jobPath, buildNum)
		if err != nil {
			return fmt.Errorf("polling build status: %w", err)
		}
		if !b.Building {
			elapsed := time.Since(start).Truncate(time.Second)
			fmt.Fprintf(os.Stderr, "Build #%d stopped — %s (%s)\n", buildNum, output.ColorStatus(b.Result), elapsed)
			return nil
		}
		if time.Since(start) > timeout {
			fmt.Fprintf(os.Stderr, "warning: build #%d still running after %s — it may have cleanup steps or finally blocks\n", buildNum, timeout)
			return nil
		}
	}
}
