package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
	"github.com/ysmaoui/jkit/internal/waiter"
)

// stagePollInterval is how often streamStageLog polls for new output.
// Overridable in tests.
var stagePollInterval = time.Second

// stuckStageCheckPolls is how many polls streamStageLog trusts an active stage
// status before it checks that the build still runs. PGV can report a stage
// of a hard-killed build, or one lost on a controller restart, as running
// forever. Overridable in tests.
var stuckStageCheckPolls = 30

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
	registerLogFlags(logCmd)
	rootCmd.AddCommand(logCmd)
}

// registerLogFlags declares the log flag surface in one place. The test harness
// resets and rebuilds subcommand flags, and when it kept its own list the two
// drifted: --tail, --head and --max-bytes were absent under test, so nothing
// could exercise them and any test using one failed with "unknown flag" rather
// than anything naming the cause.
func registerLogFlags(c *cobra.Command) {
	c.Flags().BoolP("follow", "f", false, "Follow log output")
	c.Flags().String("stage", "", "Show log for a pipeline stage (name, qualified path like \"Branch/Stage\", or ID from 'jkit stages')")
	c.Flags().String("stage-id", "", "Show log for a stage by exact node ID (from 'jkit stages')")
	c.Flags().String("grep", "", "Filter log lines matching pattern")
	c.Flags().BoolP("ignore-case", "i", false, "Case-insensitive --grep matching")
	c.Flags().Int("tail", 0, "Show only the last N lines")
	c.Flags().Int("head", 0, "Show only the first N lines")
	c.Flags().Int64("max-bytes", 50<<20, "Refuse to dump an unfiltered console larger than this (0 = unlimited)")
	c.Flags().Bool("timestamps", false, "Prefix each line with the time of day it was logged (timestamper plugin)")
	c.Flags().Bool("elapsed", false, "Prefix each line with the time elapsed since the build started (timestamper plugin)")
	c.Flags().Int("slowest", 0, "Report the N log lines with the largest time gap to the next line (timestamper plugin)")
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

// withStageHint appends how this command addresses a stage exactly, which
// ResolveStageID leaves out because it knows no CLI flags.
func withStageHint(err error, byID string) error {
	var nf *jenkins.StageNotFoundError
	var amb *jenkins.StageAmbiguousError
	switch {
	case errors.As(err, &nf):
		return fmt.Errorf("%w\nuse %s for an exact node ID", err, byID)
	case errors.As(err, &amb):
		return fmt.Errorf("%w\npass a qualified path (e.g. %q) or %s", err, amb.Example, byID)
	}
	return err
}

// stageRunning reports whether the stage may still write output, and its
// status. A stage with no final result counts as running while the build does:
// Blue Ocean reports UNKNOWN for a running stage, and both sources NOT_BUILT for
// one not started or skipped by when{}. A stage absent from the list counts as
// finished so the loop cannot spin.
func stageRunning(client *api.Client, jobPath string, buildNum int, nodeID string) (bool, string, error) {
	stages, err := client.GetPipelineStages(jobPath, buildNum)
	if err != nil {
		return false, "", err
	}
	i := slices.IndexFunc(stages, func(s jenkins.Stage) bool { return s.ID == nodeID })
	if i < 0 {
		return false, "", nil
	}
	status := stages[i].Status
	switch {
	case stageActive(status):
		return true, status, nil
	case waiter.HasResult(status):
		return false, status, nil
	}
	building, err := client.IsBuilding(jobPath, buildNum)
	return building, status, err
}

// stageActive reports a status that says the stage itself is running, which
// stageRunning trusts without asking whether the build is.
func stageActive(status string) bool {
	return status == "IN_PROGRESS" || status == "PAUSED_PENDING_INPUT" || status == "QUEUED"
}

// streamStageLog tails a single stage's log until the stage finishes or the
// context is cancelled. Every poll re-downloads the log from byte 0, so it
// stops once the output reaches the client's stage log cap rather than pull an
// ever larger prefix each second.
func streamStageLog(ctx context.Context, client *api.Client, jobPath string, buildNum int, nodeID string, w, errW io.Writer) error {
	lw := &sanitizingLineWriter{w: w}
	defer lw.Flush()
	var printed int64
	var notedNotBuilt bool
	var polls int
	copyNew := func() (capped bool, err error) {
		n, capped, err := client.CopyStageLogFrom(jobPath, buildNum, nodeID, printed, lw)
		printed += n
		if errors.Is(err, api.ErrStageLogPerStep) {
			return true, printPerStepStageLog(client, jobPath, buildNum, nodeID, printed, lw, errW, err)
		}
		if capped {
			lw.Flush()
			_, _ = fmt.Fprintf(errW, "warning: stage %s log passed %s; stopped following. "+
				"Re-run with --tail N instead of -f after the stage finishes\n", nodeID, humanBytes(int64(client.StageLogCap())))
		}
		return capped, withTimeoutHint(err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if capped, err := copyNew(); capped || err != nil {
			return err
		}
		polls++
		running, status, err := stageRunning(client, jobPath, buildNum, nodeID)
		if err != nil {
			return err
		}
		if running && stageActive(status) && polls%stuckStageCheckPolls == 0 {
			building, err := client.IsBuilding(jobPath, buildNum)
			if err != nil {
				return err
			}
			if !building {
				if capped, err := copyNew(); capped || err != nil {
					return err
				}
				_, _ = fmt.Fprintf(errW, "note: build #%d has finished but stage %s still reports %s; stopped following\n", buildNum, nodeID, status)
				return nil
			}
		}
		if running && status == "NOT_BUILT" && !notedNotBuilt {
			notedNotBuilt = true
			_, _ = fmt.Fprintf(errW, "note: stage %s has not run (NOT_BUILT); following until it starts or the build ends\n", nodeID)
		}
		if !running {
			// Output written between the read above and the stage finishing.
			_, err := copyNew()
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(stagePollInterval):
		}
	}
}

// printPerStepStageLog handles -f on a stage whose log the server serves only
// step by step, which cannot be followed by offset. A finished stage is printed
// once, as without -f; a running one is refused.
func printPerStepStageLog(client *api.Client, jobPath string, buildNum int, nodeID string, printed int64, lw *sanitizingLineWriter, errW io.Writer, perStep error) error {
	running, _, err := stageRunning(client, jobPath, buildNum, nodeID)
	if err != nil {
		return err
	}
	if running || printed > 0 {
		return fmt.Errorf("%w; following is not supported, use --tail after the stage finishes", perStep)
	}
	text, truncated, err := client.GetStageLog(jobPath, buildNum, nodeID)
	if err != nil {
		return withTimeoutHint(err)
	}
	_, _ = io.WriteString(lw, text)
	lw.Flush()
	warnStageLogTruncated(errW, nodeID, client.StageLogCap(), truncated, 0, 0, false, len(splitLogLines(text)))
	return nil
}

// maxPendingLine bounds how much of an unterminated line sanitizingLineWriter
// holds back. Past it the line is written as is, and an annotation split at
// that point may survive.
const maxPendingLine = 64 << 10

// sanitizingLineWriter sanitizes whole lines only, holding back a trailing
// partial line, so an annotation split across two reads is still stripped.
type sanitizingLineWriter struct {
	w       io.Writer
	pending []byte
}

func (l *sanitizingLineWriter) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	i := bytes.LastIndexByte(l.pending, '\n')
	if i < 0 && len(l.pending) > maxPendingLine {
		i = len(l.pending) - 1
	}
	if i >= 0 {
		if _, err := io.WriteString(l.w, output.SanitizeLog(string(l.pending[:i+1]))); err != nil {
			return 0, err
		}
		l.pending = append(l.pending[:0], l.pending[i+1:]...)
	}
	return len(p), nil
}

// Flush writes the held-back partial line.
func (l *sanitizingLineWriter) Flush() {
	if len(l.pending) > 0 {
		_, _ = io.WriteString(l.w, output.SanitizeLog(string(l.pending)))
		l.pending = l.pending[:0]
	}
}

// warnStageLogTruncated tells the user when a stage log read hit the window
// and the output may be incomplete. shown is the number of lines printed.
func warnStageLogTruncated(w io.Writer, nodeID string, window int, truncated bool, tail, head int, grep bool, shown int) {
	if !truncated {
		return
	}
	size := humanBytes(int64(window))
	switch {
	case tail > 0 && shown >= tail, head > 0 && shown >= head:
	case tail > 0 && grep:
		_, _ = fmt.Fprintf(w, "warning: stage %s log exceeds %s; --grep searched only the last %s\n", nodeID, size, size)
	case tail > 0:
		_, _ = fmt.Fprintf(w, "warning: stage %s log exceeds %s; only %d of %d lines fit in the last %s\n", nodeID, size, shown, tail, size)
	case grep:
		_, _ = fmt.Fprintf(w, "warning: stage %s log exceeds %s; --grep searched only the first %s, add --tail N to search the end\n", nodeID, size, size)
	default:
		_, _ = fmt.Fprintf(w, "warning: stage %s log exceeds %s; showing the first %s, use --tail N to read the end\n", nodeID, size, size)
	}
}

// stageLogErr reports a missing build as such: both stage log endpoints 404
// for one, which alone reads as a missing plugin.
func stageLogErr(client *api.Client, jobPath string, buildNum int, err error) error {
	if errors.Is(err, api.ErrStageLogUnavailable) {
		if berr := requireBuild(client, jobPath, buildNum); berr != nil {
			return berr
		}
	}
	return err
}

// withTimeoutHint points at --timeout when a stage log read timed out. Stage
// logs cannot be read from an offset, so a whole large log must arrive within
// the HTTP client timeout.
func withTimeoutHint(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("%w\n%s", err, stageLogTimeoutHint)
	}
	return err
}

const stageLogTimeoutHint = "the whole stage log has to download within the HTTP timeout; raise it with --timeout (e.g. --timeout 5m)"

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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

	wallClock, _ := cmd.Flags().GetBool("timestamps")
	sinceStart, _ := cmd.Flags().GetBool("elapsed")
	stampFormat, stamped, err := resolveStampFormat(cmd, wallClock, sinceStart)
	if err != nil {
		return err
	}

	slowest, _ := cmd.Flags().GetInt("slowest")
	if err := checkSlowestFlags(cmd, slowest, stamped, tail, head); err != nil {
		return err
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
	if stamped && (stageName != "" || stageID != "") {
		return fmt.Errorf("cannot use --timestamps/--elapsed with --stage/--stage-id — " +
			"stage logs come from the pipeline graph endpoint, which carries no timestamps; " +
			"run the stamped console instead and narrow it with --grep")
	}
	if stageName != "" || stageID != "" {
		nodeID := stageID
		if nodeID == "" {
			stages, err := client.GetPipelineStages(jobPath, buildNum)
			if err != nil {
				return err
			}
			if stages == nil {
				if err := requireBuild(client, jobPath, buildNum); err != nil {
					return err
				}
				return api.ErrStageLogUnavailable
			}
			nodeID, err = jenkins.ResolveStageID(stages, stageName)
			if err != nil {
				return withStageHint(err, "--stage-id <id>")
			}
		}

		grepPattern, _ := cmd.Flags().GetString("grep")
		grepI, _ := cmd.Flags().GetBool("ignore-case")

		if follow {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			err := streamStageLog(ctx, client, jobPath, buildNum, nodeID, os.Stdout, os.Stderr)
			return stageLogErr(client, jobPath, buildNum, err)
		}

		getLog := client.GetStageLog
		if tail > 0 {
			getLog = client.GetStageLogTail
		}
		log, truncated, err := getLog(jobPath, buildNum, nodeID)
		// A partial tail ends where the read failed, not where the log does,
		// so printing it as the last N lines would mislead.
		if err != nil {
			return stageLogErr(client, jobPath, buildNum, withTimeoutHint(err))
		}
		text := applyTailHead(filterLines(output.SanitizeLog(log), grepPattern, grepI), tail, head)
		fmt.Print(text)
		warnStageLogTruncated(os.Stderr, nodeID, client.StageLogCap(), truncated, tail, head, grepPattern != "", len(splitLogLines(text)))
		return nil
	}

	grepPattern, _ := cmd.Flags().GetString("grep")
	grepI, _ := cmd.Flags().GetBool("ignore-case")

	if slowest > 0 {
		isJSON, _ := cmd.Flags().GetBool("json")
		tmpl, _ := cmd.Flags().GetString("format")
		return runSlowest(client, jobPath, buildNum, slowest, isJSON, tmpl, os.Stdout, os.Stderr)
	}

	if stamped {
		return runStampedLog(client, jobPath, buildNum, stampFormat,
			grepPattern, grepI, tail, head, maxBytes, os.Stdout)
	}

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
