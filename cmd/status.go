package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var statusCmd = &cobra.Command{
	Use:   "status [job] [build#]",
	Short: "Show build status",
	Example: `  jkit status my-app
  jkit status my-app 42
  jkit status --limit 5`,
	Args: cobra.MaximumNArgs(2),
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().Int("limit", 10, "Number of recent builds to show")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	// Single build detail
	if buildNum > 0 {
		return showBuildDetail(client, f, jobPath, buildNum, isJSON, tmpl)
	}

	// List recent builds
	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		limit = 10
	}
	builds, err := client.GetBuilds(jobPath, limit)
	if err != nil {
		return err
	}

	if isJSON || tmpl != "" {
		return f.Output(builds, nil)
	}

	if len(builds) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No builds found")
		return nil
	}

	items := make([]any, len(builds))
	for i := range builds {
		items[i] = builds[i]
	}

	columns := []output.Column{
		{Header: "#", Field: func(v any) string {
			return strconv.Itoa(v.(jenkins.Build).Number)
		}},
		{Header: "RESULT", Field: func(v any) string {
			b := v.(jenkins.Build)
			if b.Building {
				return output.ColorStatus("BUILDING")
			}
			if b.Result == "" {
				return "-"
			}
			return output.ColorStatus(b.Result)
		}},
		{Header: "DURATION", Field: func(v any) string {
			d := time.Duration(v.(jenkins.Build).Duration) * time.Millisecond
			return formatDuration(d)
		}},
		{Header: "STARTED", Field: func(v any) string {
			ts := v.(jenkins.Build).Timestamp
			if ts == 0 {
				return "-"
			}
			return time.UnixMilli(ts).Format("Jan 02 15:04")
		}},
	}

	return f.Output(items, columns)
}

func showBuildDetail(client *api.Client, f *output.Formatter, jobPath string, num int, isJSON bool, tmpl string) error {
	build, err := client.GetBuild(jobPath, num)
	if err != nil {
		return err
	}

	if isJSON || tmpl != "" {
		return f.Output(build, nil)
	}

	result := build.Result
	if build.Building {
		result = "BUILDING"
	}
	d := time.Duration(build.Duration) * time.Millisecond
	started := time.UnixMilli(build.Timestamp).Format("Jan 02 15:04:05")

	_, _ = fmt.Fprintf(os.Stdout, "Build:    #%d\n", build.Number)
	_, _ = fmt.Fprintf(os.Stdout, "Result:   %s\n", output.ColorStatus(result))
	if cause := build.Cause(); cause != "" {
		_, _ = fmt.Fprintf(os.Stdout, "Cause:    %s\n", cause)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Duration: %s\n", formatDuration(d))
	_, _ = fmt.Fprintf(os.Stdout, "Started:  %s\n", started)
	_, _ = fmt.Fprintf(os.Stdout, "URL:      %s\n", build.URL)

	if params := build.Parameters(); len(params) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nParameters:")
		maxName := 0
		for _, p := range params {
			if len(p.Name) > maxName {
				maxName = len(p.Name)
			}
		}
		for _, p := range params {
			_, _ = fmt.Fprintf(os.Stdout, "  %-*s = %s\n", maxName, p.Name, p.Value())
		}
	}

	// Try to show stages
	stages, stageErr := client.GetPipelineStages(jobPath, num)
	if stageErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: could not fetch stages: %s\n", stageErr)
	}
	if len(stages) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nStages:")
		tree := jenkins.BuildStageTree(stages)
		maxName, maxStatus := 0, 0
		for _, s := range tree {
			w := len(s.Name) + s.Depth*2
			if w > maxName {
				maxName = w
			}
			if len(s.Status) > maxStatus {
				maxStatus = len(s.Status)
			}
		}
		for _, s := range tree {
			indent := strings.Repeat("  ", s.Depth)
			padded := indent + s.Name
			sd := time.Duration(s.DurationMillis) * time.Millisecond
			_, _ = fmt.Fprintf(os.Stdout, "  %-*s  %-*s  %s\n", maxName, padded, maxStatus, output.ColorStatus(s.Status), formatDuration(sd))
		}
	}

	return nil
}
