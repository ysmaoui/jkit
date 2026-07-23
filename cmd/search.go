package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Find jobs across the instance by name",
	Long: `Walk the whole Jenkins instance (or a folder subtree with --folder) and
print the full paths of jobs whose name matches a case-insensitive substring.`,
	Example: `  jkit search backend
  jkit search my-svc --folder team
  jkit search deploy --limit 50 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().String("folder", "", "Limit the search to a folder subtree")
	searchCmd.Flags().Int("limit", 0, "Maximum results to show (0 = no limit)")
	rootCmd.AddCommand(searchCmd)
}

// matchJobs returns jobs whose full name (or name) contains the pattern,
// case-insensitively.
func matchJobs(jobs []jenkins.Job, pattern string) []jenkins.Job {
	p := strings.ToLower(pattern)
	var matched []jenkins.Job
	for _, j := range jobs {
		name := j.FullName
		if name == "" {
			name = j.Name
		}
		if strings.Contains(strings.ToLower(name), p) {
			matched = append(matched, j)
		}
	}
	return matched
}

func runSearch(cmd *cobra.Command, args []string) error {
	client, _, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	folder, _ := cmd.Flags().GetString("folder")
	limit, _ := cmd.Flags().GetInt("limit")

	jobs, err := client.ListJobsRecursive(folder)
	if err != nil {
		return err
	}

	matched := matchJobs(jobs, args[0])

	omitted := 0
	if limit > 0 && len(matched) > limit {
		omitted = len(matched) - limit
		matched = matched[:limit]
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		if err := f.Output(matched, nil); err != nil {
			return err
		}
		if omitted > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "note: %d more match omitted by --limit\n", omitted)
		}
		return nil
	}

	if len(matched) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "No jobs match %q\n", args[0])
		return nil
	}

	items := make([]any, len(matched))
	for i := range matched {
		items[i] = matched[i]
	}

	columns := []output.Column{
		{Header: "JOB", Field: func(v any) string {
			j := v.(jenkins.Job)
			if j.FullName != "" {
				return j.FullName
			}
			return j.Name
		}},
		{Header: "TYPE", Field: func(v any) string {
			return v.(jenkins.Job).Kind()
		}},
		{Header: "STATUS", Field: func(v any) string {
			j := v.(jenkins.Job)
			if j.IsContainer() || j.LastBuild == nil {
				return "-"
			}
			return output.ColorStatus(colorToStatus(j.Color))
		}},
	}

	if err := f.Output(items, columns); err != nil {
		return err
	}
	if omitted > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "note: %d more match omitted by --limit\n", omitted)
	}
	return nil
}
