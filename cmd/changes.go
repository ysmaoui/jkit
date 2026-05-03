package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/jenkins"
	"github.com/ysmaoui/jk/internal/output"
)

var changesCmd = &cobra.Command{
	Use:   "changes [job] [build#]",
	Short: "Show SCM changes in a build",
	Example: `  jk changes my-app
  jk changes my-app 42`,
	Args: cobra.MaximumNArgs(2),
	RunE: runChanges,
}

func init() {
	rootCmd.AddCommand(changesCmd)
}

func runChanges(cmd *cobra.Command, args []string) error {
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

	build, err := client.GetBuild(jobPath, buildNum)
	if err != nil {
		return err
	}

	// Collect all changes from all changeSets
	var changes []jenkins.Change
	for _, cs := range build.ChangeSets {
		changes = append(changes, cs.Items...)
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(changes, nil)
	}

	if len(changes) == 0 {
		fmt.Fprintf(os.Stderr, "No SCM changes in build #%d\n", buildNum)
		return nil
	}

	rows := make([]any, len(changes))
	for i := range changes {
		rows[i] = changes[i]
	}

	columns := []output.Column{
		{Header: "COMMIT", Field: func(v any) string {
			id := v.(jenkins.Change).CommitID
			if len(id) > 7 {
				return id[:7]
			}
			return id
		}},
		{Header: "AUTHOR", Field: func(v any) string {
			return v.(jenkins.Change).Author.FullName
		}},
		{Header: "MESSAGE", Field: func(v any) string {
			msg := v.(jenkins.Change).Message
			// Take only first line
			for i, c := range msg {
				if c == '\n' || c == '\r' {
					msg = msg[:i]
					break
				}
			}
			// Truncate to 60 chars
			if len(msg) > 60 {
				msg = msg[:60] + "..."
			}
			return msg
		}},
	}

	return f.Output(rows, columns)
}
