package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/output"
)

// resolveStampFormat validates the two timestamp flags against each other and
// against --follow, and reports which clock was asked for.
func resolveStampFormat(cmd *cobra.Command, wallClock, sinceStart bool) (api.TimestampFormat, bool, error) {
	if wallClock && sinceStart {
		return "", false, fmt.Errorf("cannot use --timestamps and --elapsed together — pick one clock")
	}
	if !wallClock && !sinceStart {
		return "", false, nil
	}
	if follow, _ := cmd.Flags().GetBool("follow"); follow {
		return "", false, fmt.Errorf("cannot use --timestamps/--elapsed with --follow — " +
			"the timestamps endpoint is indexed by line and sends no end-of-stream signal, " +
			"so there is nothing to tell a live tail when to stop; follow the plain log instead")
	}
	if wallClock {
		return api.TimestampWallClock, true, nil
	}
	return api.TimestampElapsed, true, nil
}

// guardStampedSize refuses an unbounded stamped dump larger than --max-bytes.
// The timestamps endpoint reports no size header, so the raw console size stands
// in as a lower bound: adding a prefix to every line only ever makes it bigger.
func guardStampedSize(client *api.Client, jobPath string, buildNum int, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	size, err := client.GetBuildLogSize(jobPath, buildNum)
	if err != nil {
		return err
	}
	if size <= maxBytes {
		return nil
	}
	return fmt.Errorf("console log is %s before timestamps are added — refusing to dump it whole\n"+
		"  narrow with --tail N, --head N, or --grep PATTERN, redirect to a file,\n"+
		"  or pass --max-bytes 0 to override", humanBytes(size))
}

// stampedWindow maps --tail/--head onto the endpoint's line window. startLine is
// 1-based and negative counts back from the end, so both limits are served by
// the server instead of downloading the whole log. When both are given, --tail
// selects the window and --head trims it afterwards, matching the plain log.
func stampedWindow(tail, head int) (startLine, endLine int) {
	switch {
	case tail > 0:
		return -tail, 0
	case head > 0:
		return 1, head
	default:
		return 0, 0
	}
}

// runStampedLog prints the console log with a timestamp on every line.
//
// --grep cannot use the server-side line window: the endpoint filters nothing,
// so matching means reading the whole log and --tail/--head then apply to the
// matches rather than to the raw lines, exactly as they do for the plain log.
func runStampedLog(client *api.Client, jobPath string, buildNum int, format api.TimestampFormat,
	grepPattern string, ignoreCase bool, tail, head int, maxBytes int64, w io.Writer) error {
	if grepPattern != "" {
		if err := guardStampedSize(client, jobPath, buildNum, maxBytes); err != nil {
			return err
		}
		return stampedGrep(client, jobPath, buildNum, format, grepPattern, ignoreCase, tail, head, w)
	}

	startLine, endLine := stampedWindow(tail, head)
	if startLine == 0 {
		if err := guardStampedSize(client, jobPath, buildNum, maxBytes); err != nil {
			return err
		}
	}

	// With --head alone the endpoint already stopped at the right log line, and
	// counting what comes back would stop early: one log line can arrive as
	// several physical lines. Only the --tail plus --head combination needs a
	// client-side trim, and there it trims printed lines.
	trim := 0
	if tail > 0 && head > 0 {
		trim = head
	}
	printed := 0
	return client.StampedLogLines(jobPath, buildNum, format, startLine, endLine, func(line string) bool {
		_, _ = fmt.Fprintln(w, output.SanitizeLog(line))
		printed++
		return trim <= 0 || printed < trim
	})
}

// stampedGrep streams the whole stamped log and prints matching lines, keeping
// memory bounded: --tail holds a ring of the last N matches, --head stops after
// N. The pattern is matched against the whole printed line, timestamp included,
// so "--elapsed --grep 00:28" finds everything logged 28 minutes in.
func stampedGrep(client *api.Client, jobPath string, buildNum int, format api.TimestampFormat,
	pattern string, ignoreCase bool, tail, head int, w io.Writer) error {
	matches := matcher(pattern, ignoreCase)

	if tail > 0 {
		ring := make([]string, 0, tail)
		err := client.StampedLogLines(jobPath, buildNum, format, 0, 0, func(line string) bool {
			if line = output.SanitizeLog(line); matches(line) {
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
	return client.StampedLogLines(jobPath, buildNum, format, 0, 0, func(line string) bool {
		if line = output.SanitizeLog(line); matches(line) {
			_, _ = fmt.Fprintln(w, line)
			count++
			if head > 0 && count >= head {
				return false
			}
		}
		return true
	})
}
