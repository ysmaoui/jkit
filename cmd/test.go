package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var testCmd = &cobra.Command{
	Use:   "test [job] [build#]",
	Short: "Show test results",
	Example: `  jkit test my-app
  jkit test my-app 42
  jkit test my-app 42 --failed`,
	Args: cobra.MaximumNArgs(2),
	RunE: runTest,
}

func init() {
	testCmd.Flags().Bool("failed", false, "Show only failed tests")
	testCmd.Flags().Bool("new-failures", false, "Show only tests that regressed from the previous build")
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
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

	report, err := client.GetTestReport(jobPath, buildNum)
	if err != nil {
		return err
	}
	if report == nil {
		_, _ = fmt.Fprintf(os.Stderr, "No test results for build #%d\n", buildNum)
		return nil
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)
	failedOnly, _ := cmd.Flags().GetBool("failed")
	newFailures, _ := cmd.Flags().GetBool("new-failures")

	// Build previous build's test status map for --new-failures
	var prevStatus map[string]string
	if newFailures {
		prevReport, _ := client.GetTestReport(jobPath, buildNum-1)
		if prevReport == nil {
			_, _ = fmt.Fprintln(os.Stderr, "warning: no test report for previous build — showing all failures")
			failedOnly = true
			newFailures = false
		} else {
			prevStatus = make(map[string]string)
			for _, s := range prevReport.Suites {
				for _, c := range s.Cases {
					prevStatus[c.ClassName+"."+c.Name] = c.Status
				}
			}
		}
	}

	if isJSON || tmpl != "" {
		return f.Output(report, nil)
	}

	// Flatten cases
	var cases []jenkins.TestCase
	for _, suite := range report.Suites {
		for _, tc := range suite.Cases {
			if failedOnly && tc.Status == "PASSED" {
				continue
			}
			if newFailures {
				// Only show failures that were passing (or new) in previous build
				if tc.Status == "PASSED" {
					continue
				}
				prev, existed := prevStatus[tc.ClassName+"."+tc.Name]
				if existed && prev != "PASSED" {
					continue // was already failing — skip
				}
			}
			cases = append(cases, tc)
		}
	}

	if len(cases) == 0 {
		if failedOnly {
			_, _ = fmt.Fprintln(os.Stderr, "No failed tests")
		} else {
			_, _ = fmt.Fprintln(os.Stderr, "No test cases found")
		}
		return nil
	}

	rows := make([]any, len(cases))
	for i := range cases {
		rows[i] = cases[i]
	}

	columns := []output.Column{
		{Header: "CLASS", Field: func(v any) string {
			return v.(jenkins.TestCase).ClassName
		}},
		{Header: "TEST", Field: func(v any) string {
			return v.(jenkins.TestCase).Name
		}},
		{Header: "STATUS", Field: func(v any) string {
			return output.ColorStatus(v.(jenkins.TestCase).Status)
		}},
		{Header: "DURATION", Field: func(v any) string {
			d := v.(jenkins.TestCase).Duration
			if d < 1 {
				return "< 1s"
			}
			return fmt.Sprintf("%.1fs", d)
		}},
	}

	if err := f.Output(rows, columns); err != nil {
		return err
	}

	// Print error details for failed tests
	for _, tc := range cases {
		if tc.ErrorDetails != "" {
			_, _ = fmt.Fprintf(os.Stdout, "\n--- %s.%s ---\n%s\n", tc.ClassName, tc.Name, tc.ErrorDetails)
		}
	}

	// Summary
	_, _ = fmt.Fprintf(os.Stderr, "\n%d passed, %d failed, %d skipped\n", report.PassCount, report.FailCount, report.SkipCount)

	return nil
}
