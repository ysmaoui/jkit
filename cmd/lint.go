package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

var lintCmd = &cobra.Command{
	Use:   "lint [file]",
	Short: "Validate a declarative Jenkinsfile (scripted pipelines not supported)",
	Long:  "Validates a declarative Jenkinsfile against the Jenkins server's Pipeline Model Definition plugin. Only declarative syntax (pipeline { }) is supported — scripted pipelines (node { }) cannot be validated server-side.",
	Example: `  jkit lint
  jkit lint path/to/Jenkinsfile`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLint,
}

func init() {
	rootCmd.AddCommand(lintCmd)
}

func runLint(cmd *cobra.Command, args []string) error {
	file := "Jenkinsfile"
	if len(args) > 0 {
		file = args[0]
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	client, _, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}

	form := url.Values{"jenkinsfile": {string(data)}}
	body := strings.NewReader(form.Encode())

	resp, err := client.Post("/pipeline-model-converter/validate", body, "application/x-www-form-urlencoded")
	if err != nil {
		return fmt.Errorf("validating Jenkinsfile: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	result, _ := io.ReadAll(resp.Body)
	lintOutput := strings.TrimSpace(string(result))

	// Jenkins validate endpoint always returns 200; check body for success.
	if strings.Contains(strings.ToLower(lintOutput), "successfully validated") {
		fmt.Println("Jenkinsfile successfully validated")
		return nil
	}

	return &jenkins.ExitError{Code: 1, Message: lintOutput}
}
