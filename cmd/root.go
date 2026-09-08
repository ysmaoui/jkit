package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/cmd/auth"
	"github.com/ysmaoui/jkit/internal/jenkins"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:           "jkit",
	Short:         "A developer-first Jenkins CLI",
	Version:       version,
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if nc, _ := cmd.Flags().GetBool("no-color"); nc {
			_ = os.Setenv("NO_COLOR", "1")
		}
	},
}

func init() {
	rootCmd.SetVersionTemplate("jkit version {{.Version}}\n")
	registerRootFlags(rootCmd)
	rootCmd.AddCommand(auth.AuthCmd)
}

// registerRootFlags declares the global flags in one place. The test harness
// resets and rebuilds them, and when it kept its own list the two drifted:
// --branch, --verbose, --timeout and --pipeline-source were absent under test,
// so no test could exercise them and a command using one failed with "unknown
// flag" rather than anything that named the cause.
func registerRootFlags(c *cobra.Command) {
	c.PersistentFlags().String("host", "", "Jenkins host URL")
	c.PersistentFlags().String("branch", "", "Branch name for a multibranch pipeline job (e.g. feature/foo); slashes are encoded automatically")
	c.PersistentFlags().Bool("json", false, "Output as JSON")
	c.PersistentFlags().String("format", "", "Output format (Go template, use {{range .}}...{{end}} for lists)")
	c.PersistentFlags().Bool("no-color", false, "Disable color output")
	c.PersistentFlags().Bool("verbose", false, "Show HTTP request/response details")
	c.PersistentFlags().String("timeout", "30s", "HTTP client timeout")
	c.PersistentFlags().String("pipeline-source", "", "Pipeline backend: auto|pgv|blueocean (env JKIT_PIPELINE_SOURCE)")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var exitErr *jenkins.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.Message != "" {
				_, _ = fmt.Fprintln(os.Stderr, exitErr.Message)
			}
			os.Exit(exitErr.Code)
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
