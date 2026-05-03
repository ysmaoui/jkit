package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/output"
)

var logCmd = &cobra.Command{
	Use:   "log [job] [build#]",
	Short: "View build log",
	Example: `  jk log my-app
  jk log my-app 42
  jk log -f my-app`,
	Args: cobra.MaximumNArgs(2),
	RunE: runLog,
}

func init() {
	logCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	logCmd.Flags().String("stage", "", "Show log for a specific pipeline stage")
	logCmd.Flags().String("grep", "", "Filter log lines matching pattern")
	logCmd.Flags().BoolP("ignore-case", "i", false, "Case-insensitive --grep matching")
	logCmd.Flags().Int("tail", 0, "Show only the last N lines")
	logCmd.Flags().Int("head", 0, "Show only the first N lines")
	rootCmd.AddCommand(logCmd)
}

func filterLines(text, pattern string, ignoreCase bool) string {
	if pattern == "" {
		return text
	}
	if ignoreCase {
		pattern = strings.ToLower(pattern)
	}
	var result strings.Builder
	for _, line := range strings.Split(text, "\n") {
		haystack := line
		if ignoreCase {
			haystack = strings.ToLower(line)
		}
		if strings.Contains(haystack, pattern) {
			result.WriteString(line)
			result.WriteByte('\n')
		}
	}
	return result.String()
}

// applyTailHead takes log text and applies --tail/--head line limits.
func applyTailHead(text string, tail, head int) string {
	if tail == 0 && head == 0 {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if tail > 0 && tail < len(lines) {
		lines = lines[len(lines)-tail:]
	}
	if head > 0 && head < len(lines) {
		lines = lines[:head]
	}
	return strings.Join(lines, "\n") + "\n"
}

func runLog(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	tail, _ := cmd.Flags().GetInt("tail")
	head, _ := cmd.Flags().GetInt("head")
	follow, _ := cmd.Flags().GetBool("follow")

	if (tail > 0 || head > 0) && follow {
		return fmt.Errorf("cannot use --tail/--head with --follow")
	}

	// If no build number given, use latest
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

	stageName, _ := cmd.Flags().GetString("stage")
	if stageName != "" {
		stages, err := client.GetPipelineStages(jobPath, buildNum)
		if err != nil {
			return err
		}
		if stages == nil {
			return fmt.Errorf("blue ocean plugin required for stage logs")
		}

		// Find matching stage (case-insensitive)
		var nodeID string
		var available []string
		for _, s := range stages {
			available = append(available, s.Name)
			if strings.EqualFold(s.Name, stageName) {
				nodeID = s.ID
			}
		}
		if nodeID == "" {
			return fmt.Errorf("stage %q not found — available stages: %s", stageName, strings.Join(available, ", "))
		}

		log, err := client.GetStageLog(jobPath, buildNum, nodeID)
		if err != nil {
			return err
		}
		grepPattern, _ := cmd.Flags().GetString("grep")
		grepI, _ := cmd.Flags().GetBool("ignore-case")
		text := filterLines(output.SanitizeLog(log), grepPattern, grepI)
		fmt.Print(applyTailHead(text, tail, head))
		return nil
	}

	grepPattern, _ := cmd.Flags().GetString("grep")
	grepI, _ := cmd.Flags().GetBool("ignore-case")

	// Auto-follow if build in progress (unless grep/tail/head active)
	if !follow && grepPattern == "" && tail == 0 && head == 0 {
		build, err := client.GetBuild(jobPath, buildNum)
		if err != nil {
			return err
		}
		if build.Building {
			follow = true
		}
	}

	if follow && grepPattern == "" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		streamer := output.NewLogStreamer(newFetchLog(client), jobPath, buildNum, os.Stdout)
		return streamer.Stream(ctx)
	}

	// Non-follow: fetch complete log, then apply filters
	var buf strings.Builder
	var offset int64
	for {
		chunk, err := client.GetBuildLog(jobPath, buildNum, offset)
		if err != nil {
			return err
		}
		if chunk.Text != "" {
			buf.WriteString(output.SanitizeLog(chunk.Text))
		}
		offset = chunk.Offset
		if !chunk.HasMore {
			break
		}
	}

	text := filterLines(buf.String(), grepPattern, grepI)
	fmt.Print(applyTailHead(text, tail, head))
	return nil
}
