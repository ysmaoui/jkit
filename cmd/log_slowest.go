package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/output"
)

// slowGap is one reported stretch of time between two consecutive log lines.
type slowGap struct {
	Line          int    `json:"line"`
	GapMillis     int64  `json:"gapMillis"`
	ElapsedMillis int64  `json:"elapsedMillis"`
	Text          string `json:"text"`
}

// checkSlowestFlags rejects the combinations --slowest cannot honour, rather
// than accepting a flag it would then ignore.
func checkSlowestFlags(cmd *cobra.Command, slowest int, stamped bool, tail, head int) error {
	if slowest < 0 {
		return fmt.Errorf("--slowest needs a positive count")
	}
	if slowest == 0 {
		return nil
	}
	refuse := func(what, why string) error {
		return fmt.Errorf("cannot use --slowest with %s — %s", what, why)
	}
	if follow, _ := cmd.Flags().GetBool("follow"); follow {
		return refuse("--follow", "it is a one-shot ranking of the whole log, not a stream; on a running build it reports the gaps so far")
	}
	if stamped {
		return refuse("--timestamps/--elapsed", "it already reports each gap's offset from the start of the build")
	}
	if stage, _ := cmd.Flags().GetString("stage"); stage != "" {
		return refuse("--stage", slowestStageWhy)
	}
	if stageID, _ := cmd.Flags().GetString("stage-id"); stageID != "" {
		return refuse("--stage-id", slowestStageWhy)
	}
	if grep, _ := cmd.Flags().GetString("grep"); grep != "" {
		return refuse("--grep", "a gap is the time between two adjacent lines, so dropping lines would invent gaps that never happened")
	}
	if tail > 0 || head > 0 {
		return refuse("--tail/--head", "it already reports only the N largest gaps; pass --slowest N to change how many")
	}
	return nil
}

// slowestStageWhy explains the one limit worth stating in full: line times are
// build-wide and carry no stage of their own, and parallel branches interleave
// in a single console, so a per-stage window would silently mix in lines that
// belong to a sibling branch running at the same moment.
const slowestStageWhy = "console line times carry no stage, and parallel branches interleave in one log, " +
	"so any stage window would silently include lines from whatever else was running"

// largestGaps returns the n biggest jumps between consecutive line times, each
// attributed to the line that *started* the wait — the command that consumed
// the time, not the one that printed once it was over. Line numbers are 1-based
// to match the endpoint's own indexing.
func largestGaps(times []int64, n int) []slowGap {
	if len(times) < 2 || n <= 0 {
		return nil
	}
	gaps := make([]slowGap, 0, len(times)-1)
	for i := 0; i+1 < len(times); i++ {
		gaps = append(gaps, slowGap{Line: i + 1, GapMillis: times[i+1] - times[i], ElapsedMillis: times[i]})
	}
	sort.SliceStable(gaps, func(a, b int) bool { return gaps[a].GapMillis > gaps[b].GapMillis })
	if n < len(gaps) {
		gaps = gaps[:n]
	}
	return gaps
}

// runSlowest reports where a build's wall-clock time actually went.
//
// It reads the line times without the log body — on a 281k-line build that is
// ~2 MB against ~35 MB for the console — then fetches only the handful of lines
// that won, one request each.
func runSlowest(client *api.Client, jobPath string, buildNum, n int, isJSON bool, tmpl string, w, notify io.Writer) error {
	// A running build's longest wait may be the one still in progress, which has
	// no end time and so cannot appear in the ranking at all.
	if build, err := client.GetBuild(jobPath, buildNum); err == nil && build.Building {
		_, _ = fmt.Fprintf(notify, "note: %s #%d is still running — gaps cover the log so far, "+
			"and a wait that has not ended yet is not in this list\n", jobPath, buildNum)
	}

	times, err := client.BuildLineTimes(jobPath, buildNum)
	if err != nil {
		return err
	}
	if len(times) < 2 {
		return fmt.Errorf("build %s #%d has %d timestamped line(s) — too few to measure a gap",
			jobPath, buildNum, len(times))
	}

	gaps := largestGaps(times, n)
	for i := range gaps {
		physical, err := client.StampedLogWindow(jobPath, buildNum, gaps[i].Line, gaps[i].Line)
		if err != nil {
			return err
		}
		gaps[i].Text = firstFragment(physical)
	}

	f := output.NewFormatter(w, isJSON, tmpl)
	if isJSON || tmpl != "" {
		return f.Output(gaps, nil)
	}

	items := make([]any, len(gaps))
	for i := range gaps {
		items[i] = gaps[i]
	}
	columns := []output.Column{
		{Header: "GAP", Field: func(v any) string { return formatGap(v.(slowGap).GapMillis) }},
		{Header: "AT", Field: func(v any) string { return formatGap(v.(slowGap).ElapsedMillis) }},
		{Header: "LINE", Field: func(v any) string { return fmt.Sprint(v.(slowGap).Line) }},
		{Header: "TEXT", Field: func(v any) string { return v.(slowGap).Text }},
	}
	return f.Output(items, columns)
}

// formatGap keeps sub-second resolution, which formatDuration collapses to
// "< 1s". On a fast build every gap would otherwise print the same.
func formatGap(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return formatDuration(time.Duration(ms) * time.Millisecond)
}

// firstFragment renders one log line as a single table cell. A line that
// carried carriage-return fragments comes back as several physical lines, so
// only the first is shown, with an ellipsis marking what was left out rather
// than dropping it silently.
func firstFragment(physical []string) string {
	if len(physical) == 0 {
		return ""
	}
	text := strings.TrimSpace(output.SanitizeLog(stripStamp(physical[0])))
	if len(physical) > 1 {
		text += " …"
	}
	return text
}

// stripStamp drops the timestamper prefix from a line. The formats jkit asks
// for contain no space, so the first separator run ends the prefix.
func stripStamp(line string) string {
	if i := strings.Index(line, api.StampSeparator); i >= 0 {
		return line[i+len(api.StampSeparator):]
	}
	return line
}
