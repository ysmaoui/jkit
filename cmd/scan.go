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

// staleScanAge is when a scan stops being a usable answer about a branch. The
// default re-index schedule on this kind of job is hours, so a day-old scan
// means the log is describing a repository state the reader has moved past.
const staleScanAge = 24 * time.Hour

var scanCmd = &cobra.Command{
	Use:   "scan [job]",
	Short: "Show what the last branch-indexing scan did (why a branch has no job)",
	Long: `Print the indexing log of a multibranch pipeline or organization folder: which
branches and tags the last scan examined, which met the criteria, and which got
a build. It answers the half of "I pushed a branch and nothing appeared" that
jkit inspect cannot, because a rejected branch has no job to list or inspect.

Two things about this log decide how to read it. It is only the LAST scan, so a
branch pushed since is absent for a boring reason, and the output says how old
the scan is. And it is English prose written by the SCM source plugin rather
than a data format, so the default output is the log itself; --summary parses it
into a table on a best-effort basis and marks anything it cannot read.`,
	Example: `  jkit scan team/svc
  jkit scan team/svc --branch feature/x
  jkit scan team/svc --summary
  jkit scan team/svc --follow`,
	Args: cobra.MaximumNArgs(1),
	RunE: runScan,
}

func init() {
	registerScanFlags(scanCmd)
	rootCmd.AddCommand(scanCmd)
}

// registerScanFlags declares the flag surface in one place, because the test
// harness resets subcommand flags and rebuilds them.
//
// --branch shadows the global flag of the same name on purpose. Globally it
// picks a branch child job to act on; here the subject is the container, and a
// branch child has no indexing log at all, so the only useful meaning left is
// "the head I am asking about".
func registerScanFlags(c *cobra.Command) {
	c.Flags().BoolP("follow", "f", false, "Follow a scan that is still running")
	c.Flags().String("branch", "", "Show only what the scan said about this branch or tag")
	c.Flags().Bool("summary", false, "Best-effort table of head and verdict instead of the log")
	c.Flags().Int64("max-bytes", 50<<20, "Refuse to read a log larger than this (0 = unlimited)")
	c.MarkFlagsMutuallyExclusive("follow", "branch")
	c.MarkFlagsMutuallyExclusive("follow", "summary")
}

// scanReport is the --json shape. The log itself is prose with no structure to
// export, so what is exported is the best-effort parse of it, carrying the
// flags that say how far to trust it (bestEffort, parsedFrom, reportedCounts vs
// parsedCounts, and the verbatim lines of every block).
type scanReport struct {
	Job        string `json:"job"`
	Kind       string `json:"kind"`
	AgeSeconds *int64 `json:"ageSeconds,omitempty"`
	Stale      bool   `json:"stale"`
	*jenkins.ScanLog
}

func runScan(cmd *cobra.Command, args []string) error {
	client, jobPath, err := resolveContainerArgs(cmd, args)
	if err != nil {
		return err
	}
	target, err := client.ScanTarget(jobPath)
	if err != nil {
		return err
	}

	if follow, _ := cmd.Flags().GetBool("follow"); follow {
		_, _ = fmt.Fprintf(os.Stderr, "Following the indexing log of %s (%s)\n", target.JobPath, target.Kind)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		streamer := output.NewLogStreamer(newFetchScanLog(client, target), target.JobPath, 0, os.Stdout)
		return streamer.Stream(ctx)
	}

	maxBytes, _ := cmd.Flags().GetInt64("max-bytes")
	text, err := readScanLog(client, target, maxBytes)
	if err != nil {
		return err
	}
	parsed := jenkins.ParseScanLog(text)
	report := newScanReport(target, parsed, time.Now())

	branch, _ := cmd.Flags().GetString("branch")
	summary, _ := cmd.Flags().GetBool("summary")
	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")

	if branch != "" {
		heads := parsed.Find(branch)
		if len(heads) == 0 {
			return notInScanError(report, branch)
		}
		if isJSON || tmpl != "" {
			report.Heads = heads
			return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(report, nil)
		}
		printScanHeader(os.Stderr, report)
		printHeadBlocks(os.Stdout, heads)
		return nil
	}

	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(report, nil)
	}
	if summary {
		printScanSummary(os.Stdout, report)
		return nil
	}
	printScanHeader(os.Stderr, report)
	_, _ = fmt.Fprint(os.Stdout, text)
	return nil
}

// newFetchScanLog adapts the indexing log to the streamer the build console
// uses. The endpoint answers with the same X-Text-Size and X-More-Data headers,
// so following a running scan needs no second implementation.
func newFetchScanLog(client *api.Client, target *api.ScanTarget) output.FetchLogFunc {
	return func(_ string, _ int, start int64) (string, int64, bool, error) {
		chunk, err := client.GetScanLog(target, start)
		if err != nil {
			return "", 0, false, err
		}
		return output.SanitizeLog(chunk.Text), chunk.Offset, chunk.HasMore, nil
	}
}

// readScanLog reads the whole log, which every mode but --follow needs: the
// parse, the filter and the staleness note all depend on lines spread across
// it.
func readScanLog(client *api.Client, target *api.ScanTarget, maxBytes int64) (string, error) {
	if maxBytes > 0 {
		size, err := client.GetScanLogSize(target)
		if err != nil {
			return "", err
		}
		if size > maxBytes {
			return "", fmt.Errorf("the indexing log of %s is %s — refusing to read it whole\n"+
				"  pass --max-bytes 0 to override, or --follow to stream it",
				target.JobPath, humanBytes(size))
		}
	}

	var b strings.Builder
	for offset := int64(0); ; {
		chunk, err := client.GetScanLog(target, offset)
		if err != nil {
			return "", err
		}
		b.WriteString(chunk.Text)
		if chunk.Offset <= offset || !chunk.HasMore {
			// progressiveText carries the pipeline annotation markers consoleText
			// strips. Stripping them here rather than at print time keeps the
			// parser reading the same text the reader sees.
			return output.SanitizeLog(b.String()), nil
		}
		offset = chunk.Offset
	}
}

func newScanReport(target *api.ScanTarget, parsed *jenkins.ScanLog, now time.Time) *scanReport {
	r := &scanReport{Job: target.JobPath, Kind: target.Kind, ScanLog: parsed}
	if age, ok := parsed.Age(now); ok {
		secs := int64(age.Seconds())
		r.AgeSeconds = &secs
		r.Stale = age > staleScanAge
	}
	return r
}

// printScanHeader states when the scan ran and what its age means. It goes to
// stderr in the modes whose stdout is log text, so redirecting the log to a
// file does not take the warning with it.
func printScanHeader(w io.Writer, r *scanReport) {
	_, _ = fmt.Fprintf(w, "Job:      %s (%s)\n", r.Job, r.Kind)
	_, _ = fmt.Fprintf(w, "Scanned:  %s\n", scanWhen(r))
	if !r.Finished {
		_, _ = fmt.Fprintf(w, "This scan has not finished — jkit scan %s --follow to watch the rest.\n", r.Job)
	}
	_, _ = fmt.Fprintln(w, scanAgeNote(r))
}

// scanWhen renders the scan time exactly as the log wrote it, adding an age
// only where the stamp's time zone resolves. An unresolvable abbreviation would
// otherwise be read as UTC and the age silently shifted by the real offset.
func scanWhen(r *scanReport) string {
	switch {
	case r.StartedRaw == "":
		return "unknown — the log carries no start line this tool recognizes"
	case r.AgeSeconds == nil:
		return r.StartedRaw + " (age unknown: that time zone does not resolve here)"
	}
	when := fmt.Sprintf("%s (%s ago)", r.StartedRaw, scanAge(time.Duration(*r.AgeSeconds)*time.Second))
	if r.Result != "" {
		when += ", " + r.Result
	}
	return when
}

// scanAge renders how long ago a scan ran. formatDuration stops at hours, which
// suits a build duration but turns a fortnight-old scan into "291h14m".
func scanAge(d time.Duration) string {
	if d < 48*time.Hour {
		return formatDuration(d)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// scanAgeNote is the sentence that keeps "not in the log" from being read as
// "rejected". It is printed in every mode, including --branch, because that is
// exactly where the mistake gets made.
func scanAgeNote(r *scanReport) string {
	if r.Stale {
		return fmt.Sprintf("warning: this scan is %s old, so a branch pushed since then has not been examined at all —\n"+
			"         a head missing from it is not necessarily a head that was rejected.",
			scanAge(time.Duration(*r.AgeSeconds)*time.Second))
	}
	return "Only the last scan is kept: a branch pushed after that time has not been examined yet."
}

func printHeadBlocks(w io.Writer, heads []jenkins.ScanHead) {
	for i, h := range heads {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		if h.Repo != "" && len(heads) > 1 {
			_, _ = fmt.Fprintf(w, "In %s:\n", h.Repo)
		}
		for _, line := range h.Lines {
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

// notInScanError explains an absent head. The absence has three causes that
// look identical in the log, and the most common one is not rejection, so all
// three are named rather than letting the reader assume.
func notInScanError(r *scanReport, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "the last scan of %s never mentions %q.\n", r.Job, name)
	fmt.Fprintf(&b, "Scanned: %s\n", scanWhen(r))
	b.WriteString("That is one of three different things:\n")
	b.WriteString("  the head was pushed after this scan and has not been examined yet\n")
	b.WriteString("  the head does not exist on the remote, or was deleted before the scan\n")
	fmt.Fprintf(&b, "  the source words its log differently (this reads %s wording)\n", jenkins.ScanWording)
	fmt.Fprintf(&b, "The scan examined %s.", strings.Join(scanCountPhrases(r.ScanLog), ", "))
	if similar := r.Similar(name); len(similar) > 0 {
		fmt.Fprintf(&b, "\nSimilar names in it: %s", strings.Join(similar, ", "))
	}
	return &jenkins.ExitError{Code: 1, Message: b.String()}
}

func printScanSummary(w io.Writer, r *scanReport) {
	_, _ = fmt.Fprintf(w, "Job:      %s (%s)\n", r.Job, r.Kind)
	_, _ = fmt.Fprintf(w, "Scanned:  %s\n", scanWhen(r))
	if len(r.Repos) > 0 {
		_, _ = fmt.Fprintf(w, "Examined: %s\n", strings.Join(r.Repos, ", "))
	}
	if !r.Finished {
		_, _ = fmt.Fprintf(w, "This scan has not finished — jkit scan %s --follow to watch the rest.\n", r.Job)
	}

	if len(r.Heads) == 0 {
		_, _ = fmt.Fprintf(w, "\nNo \"Checking branch\" block in this log. Either the source discovered nothing,\n"+
			"or it words its log differently from %s. Run without --summary to read it.\n", jenkins.ScanWording)
	} else {
		printHeadTable(w, r.Heads)
	}
	printScanNotes(w, r)
}

func printHeadTable(w io.Writer, heads []jenkins.ScanHead) {
	showRepo := false
	repo := ""
	for i, h := range heads {
		if i > 0 && h.Repo != repo {
			showRepo = true
		}
		repo = h.Repo
	}

	rows := make([][]string, 0, len(heads))
	for _, h := range heads {
		// One row per line the log wrote about the head, rather than one joined
		// and truncated cell: "Changes detected" and "No automatic build
		// triggered" are different facts, and the second is the one a reader
		// chasing a missing build needs.
		for i, detail := range headDetails(h) {
			row := []string{h.Kind, h.Name, h.Criteria, detail}
			repo := h.Repo
			if i > 0 {
				row = []string{"", "", "", detail}
				repo = ""
			}
			if showRepo {
				row = append([]string{repo}, row...)
			}
			rows = append(rows, row)
		}
	}
	header := []string{"KIND", "HEAD", "CRITERIA", "WHAT HAPPENED"}
	if showRepo {
		header = append([]string{"REPOSITORY"}, header...)
	}

	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if n := len([]rune(cell)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	_, _ = fmt.Fprintln(w)
	printRow(w, header, widths)
	for _, row := range rows {
		printRow(w, row, widths)
	}

	printUnreadLines(w, heads)
}

// headDetails is what the table shows about a head: the recognized action
// lines, or the reason it was rejected when it never got that far.
func headDetails(h jenkins.ScanHead) []string {
	lines := h.Actions
	if len(lines) == 0 && h.Reason != "" {
		lines = []string{h.Reason}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, truncate(collapseWS(output.StripControl(l)), 70))
	}
	return out
}

func printRow(w io.Writer, row []string, widths []int) {
	var b strings.Builder
	for i, cell := range row {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(cell)
		if i < len(row)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(cell))))
		}
	}
	_, _ = fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

// printUnreadLines prints, verbatim, every line inside a block that the parser
// could not classify. A table that quietly dropped them would read as complete
// on a provider whose wording it does not know.
func printUnreadLines(w io.Writer, heads []jenkins.ScanHead) {
	var flagged []jenkins.ScanHead
	for _, h := range heads {
		if len(h.Unrecognized) > 0 {
			flagged = append(flagged, h)
		}
	}
	if len(flagged) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\nLines this parser does not recognize, verbatim (%d %s):\n", len(flagged), pluralHeads(len(flagged)))
	for _, h := range flagged {
		_, _ = fmt.Fprintf(w, "  %s %s\n", h.Kind, h.Name)
		for _, line := range h.Unrecognized {
			_, _ = fmt.Fprintf(w, "    %s\n", output.StripControl(line))
		}
	}
}

func printScanNotes(w io.Writer, r *scanReport) {
	_, _ = fmt.Fprintln(w)
	if len(r.Heads) > 0 {
		_, _ = fmt.Fprintf(w, "%d %s parsed: %s.\n", len(r.Heads), pluralHeads(len(r.Heads)), strings.Join(scanCountPhrases(r.ScanLog), ", "))
	}
	if bad := r.CountDisagreements(); len(bad) > 0 {
		_, _ = fmt.Fprintf(w, "warning: the table above is INCOMPLETE — %s. Run without --summary.\n", strings.Join(bad, "; "))
	} else if len(r.Reported) > 0 {
		_, _ = fmt.Fprintln(w, "The log's own totals agree with what was parsed.")
	}
	if n := r.UnrecognizedHeads(); n > 0 {
		verb := "carry"
		if n == 1 {
			verb = "carries"
		}
		_, _ = fmt.Fprintf(w, "warning: %d %s %s no verdict this parser knows; their lines are printed above.\n", n, pluralHeads(n), verb)
	}
	if len(r.Stray) > 0 {
		_, _ = fmt.Fprintf(w, "\nLines outside every block that this parser does not recognize (%d):\n", len(r.Stray))
		for _, line := range r.Stray {
			_, _ = fmt.Fprintf(w, "  %s\n", output.StripControl(line))
		}
	}
	_, _ = fmt.Fprintf(w, "\nBest effort: the verdicts above are read from the prose of the %s.\n"+
		"Another provider words the same verdicts differently. Run without --summary for the log itself.\n", jenkins.ScanWording)
	_, _ = fmt.Fprintln(w, scanAgeNote(r))
}

// scanCountPhrases renders what the scan examined, per kind, in a stable order.
func scanCountPhrases(l *jenkins.ScanLog) []string {
	var out []string
	for _, kind := range []string{"branch", "tag", "pull request"} {
		if n := l.Parsed[kind]; n > 0 {
			out = append(out, jenkins.PluralHeads(kind, n))
		}
	}
	if len(out) == 0 {
		return []string{"no branches, tags or pull requests"}
	}
	return out
}

func pluralHeads(n int) string {
	if n == 1 {
		return "head"
	}
	return "heads"
}
