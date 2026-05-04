package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/output"
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose [job] [build#]",
	Short: "Analyze a failed build and show failure summary",
	Long:  "Fetches build metadata, identifies failed stages, extracts error lines, and shows commits and parameters. Defaults to latest build if no build number given.",
	Example: `  jkit diagnose my-app 42
  jkit diagnose https://jenkins.example.com/job/team/job/svc/42/
  jkit diagnose my-app --json`,
	Args: cobra.MaximumNArgs(2),
	RunE: runDiagnose,
}

func init() {
	rootCmd.AddCommand(diagnoseCmd)
}

func runDiagnose(cmd *cobra.Command, args []string) error {
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

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	result, err := client.Diagnose(jobPath, buildNum)
	if err != nil {
		return err
	}

	if isJSON || tmpl != "" {
		return f.Output(result, nil)
	}

	printDiagnosis(result)
	return nil
}

func printDiagnosis(r *api.DiagnoseResult) {
	_, _ = fmt.Fprintf(os.Stdout, "Build:    #%d\n", r.Build)
	_, _ = fmt.Fprintf(os.Stdout, "Result:   %s\n", output.ColorStatus(r.Result))
	_, _ = fmt.Fprintf(os.Stdout, "Duration: %s\n", r.Duration)
	if r.Cause != "" {
		_, _ = fmt.Fprintf(os.Stdout, "Cause:    %s\n", r.Cause)
	}
	if r.URL != "" {
		_, _ = fmt.Fprintf(os.Stdout, "URL:      %s\n", r.URL)
	}

	if len(r.Parameters) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nParameters:")
		for k, v := range r.Parameters {
			_, _ = fmt.Fprintf(os.Stdout, "  %s = %s\n", k, v)
		}
	}

	if r.Result == "SUCCESS" || r.Result == "BUILDING" {
		if r.Result == "SUCCESS" {
			_, _ = fmt.Fprintln(os.Stdout, "\nBuild succeeded.")
		}
		return
	}

	if len(r.FailedStages) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nFailed Stages:")
		for _, fs := range r.FailedStages {
			_, _ = fmt.Fprintf(os.Stdout, "\n  --- %s ---\n", fs.Name)
			if len(fs.Errors) == 0 {
				_, _ = fmt.Fprintln(os.Stdout, "  (no error lines extracted)")
			} else {
				for _, e := range fs.Errors {
					// Indent and truncate long lines
					line := e
					if len(line) > 200 {
						line = line[:200] + "..."
					}
					_, _ = fmt.Fprintf(os.Stdout, "  %s\n", line)
				}
			}
		}
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "\nNo failed stages identified (non-pipeline job or stages unavailable)")
	}

	if len(r.Commits) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nCommits:")
		for _, c := range r.Commits {
			msg := c.Message
			if len(msg) > 70 {
				msg = msg[:70] + "..."
			}
			_, _ = fmt.Fprintf(os.Stdout, "  %s %s — %s\n", c.Hash, c.Author, msg)
		}
	}

	// Summary hint
	if len(r.FailedStages) > 0 {
		names := make([]string, len(r.FailedStages))
		for i, fs := range r.FailedStages {
			names[i] = fs.Name
		}
		_, _ = fmt.Fprintf(os.Stdout, "\nUse 'jkit log <job> %d --stage \"%s\"' for full stage log\n", r.Build, names[0])
	}
}
