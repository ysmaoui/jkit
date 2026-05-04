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
	rootCmd.PersistentFlags().String("host", "", "Jenkins host URL")
	rootCmd.PersistentFlags().Bool("json", false, "Output as JSON")
	rootCmd.PersistentFlags().String("format", "", "Output format (Go template, use {{range .}}...{{end}} for lists)")
	rootCmd.PersistentFlags().Bool("no-color", false, "Disable color output")
	rootCmd.PersistentFlags().Bool("verbose", false, "Show HTTP request/response details")
	rootCmd.PersistentFlags().String("timeout", "30s", "HTTP client timeout")
	rootCmd.PersistentFlags().String("pipeline-source", "", "Pipeline backend: auto|pgv|blueocean (env JKIT_PIPELINE_SOURCE)")
	rootCmd.AddCommand(auth.AuthCmd)
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
