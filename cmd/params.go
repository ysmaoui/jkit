package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var paramsCmd = &cobra.Command{
	Use:   "params [job]",
	Short: "List the parameters a job accepts",
	Long: `List the build parameters a job accepts, with their type, default value,
and (for choice parameters) allowed values — so you know what to pass to
'jkit run -p KEY=VALUE' without opening the Jenkins UI.`,
	Example: `  jkit params my-app
  jkit params team/backend/my-service
  jkit params my-app --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runParams,
}

func init() {
	rootCmd.AddCommand(paramsCmd)
}

func runParams(cmd *cobra.Command, args []string) error {
	client, jobPath, _, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	params, err := client.GetJobParameters(jobPath)
	if err != nil {
		return err
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(params, nil)
	}

	if len(params) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No parameters — job is not parameterized")
		return nil
	}

	items := make([]any, len(params))
	for i := range params {
		items[i] = params[i]
	}

	columns := []output.Column{
		{Header: "NAME", Field: func(v any) string {
			return v.(jenkins.ParameterDefinition).Name
		}},
		{Header: "TYPE", Field: func(v any) string {
			return v.(jenkins.ParameterDefinition).Kind()
		}},
		{Header: "DEFAULT", Field: func(v any) string {
			d := v.(jenkins.ParameterDefinition).DefaultString()
			if d == "" {
				return "-"
			}
			return truncate(collapseWS(d), 40)
		}},
		{Header: "CHOICES", Field: func(v any) string {
			c := v.(jenkins.ParameterDefinition).Choices()
			if len(c) == 0 {
				return "-"
			}
			return strings.Join(c, ", ")
		}},
		{Header: "DESCRIPTION", Field: func(v any) string {
			return truncate(collapseWS(v.(jenkins.ParameterDefinition).Description), 60)
		}},
	}

	return f.Output(items, columns)
}

// collapseWS flattens newlines and runs of whitespace to single spaces so a
// multi-line description stays on one table row.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate shortens s to at most max runes, appending an ellipsis when cut.
// It is rune-safe (never splits a multibyte character).
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
