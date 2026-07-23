package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/output"
)

var envCmd = &cobra.Command{
	Use:   "env [job] [build#]",
	Short: "Show a build's injected environment variables",
	Long: `Dump the environment variables injected into a build (via the EnvInject
plugin's /injectedEnvVars endpoint) — useful for answering "why did this build
behave differently". When build# is omitted, the last build is used.

Secret-looking values (PASSWORD, TOKEN, SECRET, …) are masked by default;
pass --show-secrets to reveal them.`,
	Example: `  jkit env my-app
  jkit env my-app 42
  jkit env my-app 42 --filter GIT
  jkit env my-app 42 --json`,
	Args: cobra.MaximumNArgs(2),
	RunE: runEnv,
}

func init() {
	envCmd.Flags().String("filter", "", "Only show vars whose name contains this substring (case-insensitive)")
	envCmd.Flags().Bool("show-secrets", false, "Do not mask secret-looking values")
	rootCmd.AddCommand(envCmd)
}

func runEnv(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, true)
	if err != nil {
		return err
	}

	// Default to the last build when no build number is given.
	if buildNum <= 0 {
		job, err := client.GetJob(jobPath)
		if err != nil {
			return err
		}
		if job.LastBuild == nil {
			_, _ = fmt.Fprintf(os.Stderr, "No builds found for %s\n", jobPath)
			return nil
		}
		buildNum = job.LastBuild.Number
	}

	envMap, err := client.GetBuildEnv(jobPath, buildNum)
	if err != nil {
		return err
	}

	filter := strings.ToLower(mustString(cmd, "filter"))
	showSecrets, _ := cmd.Flags().GetBool("show-secrets")

	// Sort keys for stable output.
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		if filter != "" && !strings.Contains(strings.ToLower(k), filter) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(keys))
	for _, k := range keys {
		v := envMap[k]
		if !showSecrets && output.IsSecretKey(k) {
			v = output.MaskSecret(v)
		}
		out[k] = v
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(out, nil)
	}

	if len(keys) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No environment variables")
		return nil
	}

	for _, k := range keys {
		_, _ = fmt.Fprintf(os.Stdout, "%s=%s\n", k, out[k])
	}
	return nil
}

func mustString(cmd *cobra.Command, name string) string {
	s, _ := cmd.Flags().GetString(name)
	return s
}
