package cmd

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var historyCmd = &cobra.Command{
	Use:   "history [job]",
	Short: "Show a job's build result and duration trend",
	Long: `Show the last N builds of a job with a trend summary: success rate over
the window and how the most recent build's duration compares to the median.`,
	Example: `  jkit history my-app
  jkit history my-app --limit 50
  jkit history my-app --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHistory,
}

func init() {
	historyCmd.Flags().Int("limit", 20, "Number of recent builds to include")
	rootCmd.AddCommand(historyCmd)
}

func runHistory(cmd *cobra.Command, args []string) error {
	client, jobPath, _, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		limit = 20
	}

	builds, err := client.GetBuildHistory(jobPath, limit)
	if err != nil {
		return err
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(builds, nil)
	}

	if len(builds) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No builds found")
		return nil
	}

	items := make([]any, len(builds))
	for i := range builds {
		items[i] = builds[i]
	}

	columns := []output.Column{
		{Header: "#", Field: func(v any) string {
			return strconv.Itoa(v.(jenkins.Build).Number)
		}},
		{Header: "RESULT", Field: func(v any) string {
			b := v.(jenkins.Build)
			if b.Building {
				return output.ColorStatus("BUILDING")
			}
			if b.Result == "" {
				return "-"
			}
			return output.ColorStatus(b.Result)
		}},
		{Header: "DURATION", Field: func(v any) string {
			return formatDuration(time.Duration(v.(jenkins.Build).Duration) * time.Millisecond)
		}},
		{Header: "STARTED", Field: func(v any) string {
			ts := v.(jenkins.Build).Timestamp
			if ts == 0 {
				return "-"
			}
			return time.UnixMilli(ts).Format("Jan 02 15:04")
		}},
		{Header: "CAUSE", Field: func(v any) string {
			c := v.(jenkins.Build).Cause()
			if c == "" {
				return "-"
			}
			return c
		}},
	}

	if err := f.Output(items, columns); err != nil {
		return err
	}

	printHistorySummary(os.Stdout, builds)
	return nil
}

// historyStats summarizes a window of builds.
type historyStats struct {
	Completed   int
	Successful  int
	MedianMS    int64
	LastMS      int64
	HasLast     bool
	DeltaPct    int // last vs median, percent; valid only when HasLast && MedianMS>0
	HasDeltaPct bool
}

// computeHistoryStats derives success rate and duration trend from a
// newest-first slice of builds. In-progress and zero-duration builds are
// excluded from the duration median.
func computeHistoryStats(builds []jenkins.Build) historyStats {
	var s historyStats
	var durations []int64
	for _, b := range builds {
		if b.Building {
			continue
		}
		s.Completed++
		if b.Result == "SUCCESS" {
			s.Successful++
		}
		if b.Duration > 0 {
			durations = append(durations, b.Duration)
			// builds are newest-first, so the first completed one is the latest
			if !s.HasLast {
				s.LastMS = b.Duration
				s.HasLast = true
			}
		}
	}
	if len(durations) > 0 {
		sorted := make([]int64, len(durations))
		copy(sorted, durations)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		mid := len(sorted) / 2
		if len(sorted)%2 == 1 {
			s.MedianMS = sorted[mid]
		} else {
			s.MedianMS = (sorted[mid-1] + sorted[mid]) / 2
		}
	}
	if s.HasLast && s.MedianMS > 0 {
		s.DeltaPct = int(float64(s.LastMS-s.MedianMS) / float64(s.MedianMS) * 100)
		s.HasDeltaPct = true
	}
	return s
}

func printHistorySummary(w *os.File, builds []jenkins.Build) {
	s := computeHistoryStats(builds)
	if s.Completed == 0 {
		return
	}
	rate := float64(s.Successful) / float64(s.Completed) * 100
	_, _ = fmt.Fprintf(w, "\nSuccess rate: %d/%d (%.0f%%)", s.Successful, s.Completed, rate)
	if s.MedianMS > 0 {
		_, _ = fmt.Fprintf(w, "   Median duration: %s", formatDuration(time.Duration(s.MedianMS)*time.Millisecond))
	}
	if s.HasDeltaPct {
		sign := "+"
		if s.DeltaPct < 0 {
			sign = ""
		}
		_, _ = fmt.Fprintf(w, "   Last vs median: %s%d%%", sign, s.DeltaPct)
	}
	_, _ = fmt.Fprintln(w)
}
