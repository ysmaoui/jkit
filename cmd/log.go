package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

// stagePollInterval is how often streamStageLog polls for new output.
// Overridable in tests.
var stagePollInterval = time.Second

var logCmd = &cobra.Command{
	Use:   "log [job] [build#]",
	Short: "View build log",
	Example: `  jkit log my-app
  jkit log my-app 42
  jkit log -f my-app`,
	Args: cobra.MaximumNArgs(2),
	RunE: runLog,
}

func init() {
	logCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	logCmd.Flags().String("stage", "", "Show log for a specific pipeline stage (name or qualified path, e.g. \"Branch/Stage\")")
	logCmd.Flags().String("stage-id", "", "Show log for a stage by exact node ID (from 'jkit stages')")
	logCmd.Flags().String("grep", "", "Filter log lines matching pattern")
	logCmd.Flags().BoolP("ignore-case", "i", false, "Case-insensitive --grep matching")
	logCmd.Flags().Int("tail", 0, "Show only the last N lines")
	logCmd.Flags().Int("head", 0, "Show only the first N lines")
	logCmd.Flags().Int64("max-bytes", 50<<20, "Refuse to dump an unfiltered console larger than this (0 = unlimited)")
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

// resolveStageID maps a user-supplied stage name or qualified path to a unique
// node ID. It matches qualified paths first (e.g. "RemoteExec/Run Bazel Build"),
// then bare stage names. An ambiguous bare name returns an error listing every
// candidate's qualified path and ID so the caller can pick one.
func resolveStageID(stages []jenkins.Stage, input string) (string, error) {
	paths := jenkins.QualifiedStagePaths(stages)

	var pathMatches, nameMatches []jenkins.Stage
	for _, s := range stages {
		if strings.EqualFold(paths[s.ID], input) {
			pathMatches = append(pathMatches, s)
		}
		if strings.EqualFold(s.Name, input) {
			nameMatches = append(nameMatches, s)
		}
	}

	if len(pathMatches) == 1 {
		return pathMatches[0].ID, nil
	}
	if len(pathMatches) == 0 && len(nameMatches) == 1 {
		return nameMatches[0].ID, nil
	}

	// Determine candidate set for messaging.
	candidates := pathMatches
	if len(candidates) == 0 {
		candidates = nameMatches
	}
	if len(candidates) == 0 {
		available := make([]string, 0, len(stages))
		for _, s := range stages {
			available = append(available, paths[s.ID])
		}
		return "", fmt.Errorf("stage %q not found — available stages: %s", input, strings.Join(available, ", "))
	}

	var b strings.Builder
	for _, s := range candidates {
		status := s.Status
		if status == "" {
			status = "?"
		}
		fmt.Fprintf(&b, "\n  %s  (id=%s, %s)", paths[s.ID], s.ID, status)
	}
	return "", fmt.Errorf("stage %q is ambiguous — matches multiple stages:%s\npass a qualified path (e.g. %q) or --stage-id <id>",
		input, b.String(), paths[candidates[0].ID])
}

// stageRunning reports whether the stage with the given ID is still in a
// non-terminal state in the supplied stage list. A stage absent from the list
// (or in an unknown state) is treated as finished to avoid looping forever.
func stageRunning(stages []jenkins.Stage, nodeID string) bool {
	for _, s := range stages {
		if s.ID == nodeID {
			switch s.Status {
			case "IN_PROGRESS", "PAUSED_PENDING_INPUT", "QUEUED":
				return true
			}
			return false
		}
	}
	return false
}

// streamStageLog tails a single stage's log until the stage finishes or the
// context is cancelled. PGV/Blue Ocean stage-log endpoints return the full log
// per call, so we print only the bytes appended since the previous poll.
func streamStageLog(ctx context.Context, client *api.Client, jobPath string, buildNum int, nodeID string, w io.Writer) error {
	var printed int
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		raw, err := client.GetStageLog(jobPath, buildNum, nodeID)
		if err != nil {
			return err
		}
		if len(raw) > printed {
			_, _ = fmt.Fprint(w, output.SanitizeLog(raw[printed:]))
			printed = len(raw)
		}

		stages, err := client.GetPipelineStages(jobPath, buildNum)
		if err != nil {
			return err
		}
		if !stageRunning(stages, nodeID) {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(stagePollInterval):
		}
	}
}

func runLog(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	tail, _ := cmd.Flags().GetInt("tail")
	head, _ := cmd.Flags().GetInt("head")
	maxBytes, _ := cmd.Flags().GetInt64("max-bytes")
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
	stageID, _ := cmd.Flags().GetString("stage-id")
	if stageName != "" && stageID != "" {
		return fmt.Errorf("cannot use --stage and --stage-id together")
	}
	if stageName != "" || stageID != "" {
		nodeID := stageID
		if nodeID == "" {
			stages, err := client.GetPipelineStages(jobPath, buildNum)
			if err != nil {
				return err
			}
			if stages == nil {
				return fmt.Errorf("blue ocean plugin required for stage logs")
			}
			nodeID, err = resolveStageID(stages, stageName)
			if err != nil {
				return err
			}
		}

		grepPattern, _ := cmd.Flags().GetString("grep")
		grepI, _ := cmd.Flags().GetBool("ignore-case")

		if follow {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			return streamStageLog(ctx, client, jobPath, buildNum, nodeID, os.Stdout)
		}

		log, err := client.GetStageLog(jobPath, buildNum, nodeID)
		if err != nil {
			return err
		}
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

	// Completed build (or one-shot with filters): stream the log rather than
	// buffering it whole, so a multi-hundred-MB console neither blows up memory
	// nor gets silently truncated.
	switch {
	case grepPattern != "":
		return runConsoleGrep(client, jobPath, buildNum, grepPattern, grepI, tail, head, os.Stdout)

	case tail > 0:
		lines, err := consoleTailLines(client, jobPath, buildNum, tail)
		if err != nil {
			return err
		}
		if head > 0 && head < len(lines) {
			lines = lines[:head]
		}
		for _, l := range lines {
			fmt.Println(l)
		}
		return nil

	case head > 0:
		printed := 0
		return forEachLogLine(client, jobPath, buildNum, func(line string) bool {
			fmt.Println(line)
			printed++
			return printed < head
		})

	default:
		// Unfiltered full dump: refuse a giant console unless explicitly allowed.
		size, err := client.GetBuildLogSize(jobPath, buildNum)
		if err != nil {
			return err
		}
		if maxBytes > 0 && size > maxBytes {
			return fmt.Errorf("console log is %s — refusing to dump it whole\n"+
				"  narrow with --tail N, --head N, or --grep PATTERN, redirect to a file,\n"+
				"  or pass --max-bytes 0 to override", humanBytes(size))
		}
		return streamConsoleChunks(client, jobPath, buildNum, os.Stdout)
	}
}

// matcher returns a line predicate matching the existing --grep substring
// semantics (optionally case-insensitive).
func matcher(pattern string, ignoreCase bool) func(string) bool {
	if ignoreCase {
		pattern = strings.ToLower(pattern)
	}
	return func(line string) bool {
		if ignoreCase {
			return strings.Contains(strings.ToLower(line), pattern)
		}
		return strings.Contains(line, pattern)
	}
}

// forEachLogLine streams the console log line by line with bounded memory,
// carrying partial lines across chunk boundaries and sanitizing each complete
// line. fn returns false to stop early. It stops at end-of-log or once the
// server reports no forward progress (caught up to a still-running build).
func forEachLogLine(client *api.Client, jobPath string, buildNum int, fn func(line string) bool) error {
	var offset int64
	var carry strings.Builder
	for {
		chunk, err := client.GetBuildLog(jobPath, buildNum, offset)
		if err != nil {
			return err
		}
		carry.WriteString(chunk.Text)
		data := carry.String()
		idx := 0
		for {
			nl := strings.IndexByte(data[idx:], '\n')
			if nl < 0 {
				break
			}
			if !fn(output.SanitizeLog(data[idx : idx+nl])) {
				return nil
			}
			idx += nl + 1
		}
		carry.Reset()
		carry.WriteString(data[idx:])

		advanced := chunk.Offset > offset
		offset = chunk.Offset
		if !chunk.HasMore || !advanced {
			if carry.Len() > 0 {
				fn(output.SanitizeLog(carry.String()))
			}
			return nil
		}
	}
}

// streamConsoleChunks writes the full console log to w in bounded-memory chunks.
func streamConsoleChunks(client *api.Client, jobPath string, buildNum int, w io.Writer) error {
	var offset int64
	for {
		chunk, err := client.GetBuildLog(jobPath, buildNum, offset)
		if err != nil {
			return err
		}
		if chunk.Text != "" {
			_, _ = fmt.Fprint(w, output.SanitizeLog(chunk.Text))
		}
		advanced := chunk.Offset > offset
		offset = chunk.Offset
		if !chunk.HasMore || !advanced {
			return nil
		}
	}
}

// runConsoleGrep streams the log and prints matching lines. With tail>0 it keeps
// only the last N matches (ring buffer); with head>0 (and no tail) it stops
// after N matches. Memory stays bounded regardless of total log size.
func runConsoleGrep(client *api.Client, jobPath string, buildNum int, pattern string, ignoreCase bool, tail, head int, w io.Writer) error {
	match := matcher(pattern, ignoreCase)

	if tail > 0 {
		ring := make([]string, 0, tail)
		err := forEachLogLine(client, jobPath, buildNum, func(line string) bool {
			if match(line) {
				if len(ring) == tail {
					ring = ring[1:]
				}
				ring = append(ring, line)
			}
			return true
		})
		if err != nil {
			return err
		}
		if head > 0 && head < len(ring) {
			ring = ring[:head]
		}
		for _, l := range ring {
			_, _ = fmt.Fprintln(w, l)
		}
		return nil
	}

	count := 0
	return forEachLogLine(client, jobPath, buildNum, func(line string) bool {
		if match(line) {
			_, _ = fmt.Fprintln(w, line)
			count++
			if head > 0 && count >= head {
				return false
			}
		}
		return true
	})
}

// consoleTailLines returns the last n lines of the console, fetching only a
// tail window (not the whole log) and growing it when the window holds fewer
// than n lines (e.g. very long lines).
func consoleTailLines(client *api.Client, jobPath string, buildNum, n int) ([]string, error) {
	size, err := client.GetBuildLogSize(jobPath, buildNum)
	if err != nil {
		return nil, err
	}
	const maxWindow = 64 << 20 // 64 MB
	window := int64(2 << 20)   // 2 MB
	for {
		text, err := client.GetBuildLogTail(jobPath, buildNum, window)
		if err != nil {
			return nil, err
		}
		lines := splitLogLines(output.SanitizeLog(text))
		if len(lines) >= n || window >= size || window >= maxWindow {
			if n < len(lines) {
				lines = lines[len(lines)-n:]
			}
			return lines, nil
		}
		window *= 2
	}
}

// splitLogLines splits text into lines, dropping a single trailing newline so a
// log ending in "\n" doesn't yield a phantom empty last line.
func splitLogLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// humanBytes formats a byte count as a human-readable size.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
