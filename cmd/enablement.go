package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var disableCmd = &cobra.Command{
	Use:     "disable [job]",
	Short:   "Stop a job from building",
	Long:    "Disables a job so Jenkins stops building it. Reversible with 'jkit enable'. Needs the Job/Configure permission; it changes the job's run state and never writes its definition.",
	Example: "  jkit disable my-app\n  jkit disable https://jenkins.example.com/job/team/job/my-app/",
	Args:    cobra.MaximumNArgs(1),
	RunE:    func(cmd *cobra.Command, args []string) error { return runSetEnabled(cmd, args, false) },
}

var enableCmd = &cobra.Command{
	Use:     "enable [job]",
	Short:   "Let a disabled job build again",
	Long:    "Enables a job that was disabled. Needs the Job/Configure permission; it changes the job's run state and never writes its definition.",
	Example: "  jkit enable my-app\n  jkit enable team/backend/my-service",
	Args:    cobra.MaximumNArgs(1),
	RunE:    func(cmd *cobra.Command, args []string) error { return runSetEnabled(cmd, args, true) },
}

func init() {
	rootCmd.AddCommand(disableCmd)
	rootCmd.AddCommand(enableCmd)
}

func runSetEnabled(cmd *cobra.Command, args []string, enable bool) error {
	client, jobPath, _, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	before, err := client.GetJobEnablement(jobPath)
	if err != nil {
		return err
	}
	if !before.Supported {
		return fmt.Errorf("%s has no enabled state to change: a %s cannot be disabled", jobPath, jenkins.KindFromClass(before.Class))
	}
	if before.Disabled == !enable {
		_, _ = fmt.Fprintf(os.Stderr, "%s is already %s\n", jobPath, stateWord(before.Disabled))
		return outputEnablement(cmd, before)
	}

	after, err := client.SetJobEnabled(jobPath, enable)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stderr, "%s is now %s\n", jobPath, stateWord(after.Disabled))
	warnIfBranchChild(jobPath, after)
	return outputEnablement(cmd, after)
}

// warnIfBranchChild flags that re-indexing rewrites a branch job's config, so a
// toggle made here may not survive the next scan. The parent is named because
// that is where a lasting change belongs.
func warnIfBranchChild(jobPath string, st *api.JobEnablement) {
	if !strings.Contains(st.Class, "WorkflowJob") || !strings.Contains(jobPath, "%2F") {
		return
	}
	parent := jobPath[:strings.LastIndex(jobPath, "/")]
	_, _ = fmt.Fprintf(os.Stderr,
		"warning: this looks like a branch of %s, whose config every scan rewrites, so the next indexing run may undo this. To stop a branch building for good, change the discovery rules on the parent.\n",
		parent)
}

func outputEnablement(cmd *cobra.Command, st *api.JobEnablement) error {
	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(st, nil)
	}
	return nil
}

func stateWord(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return "enabled"
}
