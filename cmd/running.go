package cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var runningCmd = &cobra.Command{
	Use:   "running",
	Short: "List builds currently running",
	Example: `  jkit running
  jkit running --folder team/svc
  jkit running --json`,
	Args: cobra.NoArgs,
	RunE: runRunning,
}

func init() {
	registerRunningFlags(runningCmd)
	rootCmd.AddCommand(runningCmd)
}

func registerRunningFlags(cmd *cobra.Command) {
	cmd.Flags().String("folder", "", "Only builds of jobs in this folder (or this job)")
}

func runRunning(cmd *cobra.Command, args []string) error {
	client, _, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}

	builds, err := client.RunningBuilds()
	if err != nil {
		return err
	}
	folder, _ := cmd.Flags().GetString("folder")
	builds = api.FilterRunningByFolder(builds, folder)

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(builds, nil)
	}

	if len(builds) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No running builds")
		return nil
	}

	now := time.Now()
	rows := make([]any, len(builds))
	for i := range builds {
		rows[i] = builds[i]
	}
	columns := []output.Column{
		{Header: "JOB", Field: func(v any) string { return v.(jenkins.RunningBuild).Job }},
		{Header: "BUILD", Field: func(v any) string {
			return "#" + strconv.Itoa(v.(jenkins.RunningBuild).Number)
		}},
		{Header: "ELAPSED", Field: func(v any) string {
			ts := v.(jenkins.RunningBuild).Timestamp
			if ts <= 0 {
				return "-"
			}
			return formatDuration(now.Sub(time.UnixMilli(ts)))
		}},
		{Header: "URL", Field: func(v any) string { return v.(jenkins.RunningBuild).URL }},
	}
	return f.Output(rows, columns)
}
