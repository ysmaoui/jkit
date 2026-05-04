package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List Jenkins jobs",
	Example: `  jkit list
  jkit list --folder team/frontend
  jkit list --json`,
	RunE: runList,
}

func init() {
	listCmd.Flags().String("folder", "", "Folder path to list")
	listCmd.Flags().BoolP("recursive", "r", false, "List jobs recursively across all folders")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	client, _, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	folder, _ := cmd.Flags().GetString("folder")
	recursive, _ := cmd.Flags().GetBool("recursive")

	var jobs []jenkins.Job
	var err2 error
	if recursive {
		jobs, err2 = client.ListJobsRecursive(folder)
	} else {
		jobs, err2 = client.ListJobs(folder)
	}
	if err2 != nil {
		return err2
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(jobs, nil)
	}

	if len(jobs) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No jobs found")
		return nil
	}

	items := make([]any, len(jobs))
	for i := range jobs {
		items[i] = jobs[i]
	}

	columns := []output.Column{
		{Header: "NAME", Field: func(v any) string {
			j := v.(jenkins.Job)
			name := j.Name
			if recursive && j.FullName != "" {
				name = j.FullName
			}
			if j.IsFolder() {
				return name + "/"
			}
			return name
		}},
		{Header: "STATUS", Field: func(v any) string {
			j := v.(jenkins.Job)
			if j.IsFolder() {
				return "-"
			}
			return output.ColorStatus(colorToStatus(j.Color))
		}},
		{Header: "LAST BUILD", Field: func(v any) string {
			j := v.(jenkins.Job)
			if j.IsFolder() || j.LastBuild == nil {
				return "-"
			}
			result := j.LastBuild.Result
			if result == "" {
				result = "BUILDING"
			}
			return fmt.Sprintf("#%d %s", j.LastBuild.Number, output.ColorStatus(result))
		}},
	}

	return f.Output(items, columns)
}

func colorToStatus(color string) string {
	switch color {
	case "blue":
		return "SUCCESS"
	case "red":
		return "FAILURE"
	case "yellow":
		return "UNSTABLE"
	case "aborted":
		return "ABORTED"
	case "disabled", "grey":
		return "DISABLED"
	case "blue_anime", "red_anime", "yellow_anime", "grey_anime", "disabled_anime", "aborted_anime", "notbuilt_anime":
		return "BUILDING"
	case "notbuilt":
		return "NOT_BUILT"
	default:
		return color
	}
}
