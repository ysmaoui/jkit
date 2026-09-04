package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

func runInspectHistory(cmd *cobra.Command, client *api.Client, jobPath string) error {
	entries, err := client.GetJobConfigHistory(jobPath)
	if err != nil {
		return err
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		// Structured output stays complete: collapsing is a reading aid.
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(entries, nil)
	}

	showSystem, _ := cmd.Flags().GetBool("show-system")
	return printConfigHistory(os.Stdout, os.Stderr, jobPath, entries, showSystem)
}

// configHistoryRow is one printed line: a single change, or a run of
// consecutive automated writes folded together.
type configHistoryRow struct {
	When      string
	Operation string
	User      string
	Detail    string
}

func printConfigHistory(w, warn io.Writer, jobPath string, entries []jenkins.ConfigChange, showSystem bool) error {
	if len(entries) == 0 {
		_, _ = fmt.Fprintf(warn, "No config history for %s — either nothing has changed since the plugin started recording, or you cannot see it: without the Job/Configure or Job/ExtendedRead permission the plugin returns an empty list rather than refusing.\n", jobPath)
		return nil
	}

	rows, collapsed := configHistoryRows(entries, showSystem)
	items := make([]any, len(rows))
	for i := range rows {
		items[i] = rows[i]
	}

	columns := []output.Column{
		{Header: "WHEN", Field: func(v any) string { return v.(configHistoryRow).When }},
		{Header: "OPERATION", Field: func(v any) string { return v.(configHistoryRow).Operation }},
		{Header: "USER", Field: func(v any) string { return v.(configHistoryRow).User }},
		{Header: "DETAIL", Field: func(v any) string {
			d := v.(configHistoryRow).Detail
			if d == "" {
				return "-"
			}
			return d
		}},
	}
	if err := output.NewFormatter(w, false, "").Output(items, columns); err != nil {
		return err
	}

	if collapsed > 0 {
		_, _ = fmt.Fprintf(w, "\n%d of %d entries are folded into the runs above, all repeated automated writes (re-indexing rewrites a branch job's config on every scan); --show-system lists them one by one.\n", collapsed, len(entries))
	}
	_, _ = fmt.Fprintf(w, "\nRetained entries: %d. The plugin caps how many it keeps per job and the server can truncate the response without saying so, so this is what it still holds, not every change ever made.\n", len(entries))
	return nil
}

// configHistoryRows renders entries newest first, folding each run of two or
// more consecutive automated writes into a single row. Without that, a
// multibranch branch job shows nothing but re-indexing churn. Returns the rows
// and how many entries the folding hid.
func configHistoryRows(entries []jenkins.ConfigChange, showSystem bool) ([]configHistoryRow, int) {
	rows := make([]configHistoryRow, 0, len(entries))
	collapsed := 0
	for i := 0; i < len(entries); {
		if showSystem || !entries[i].BySystem() {
			rows = append(rows, singleChangeRow(entries[i]))
			i++
			continue
		}
		// A run may only fold entries that say the same thing and carry nothing
		// of their own. Indexing writes Created and Renamed through the same
		// path as Changed, so folding on authorship alone erases them and the
		// footer then asserts they were scan churn.
		run := i
		for run < len(entries) && entries[run].BySystem() &&
			entries[run].Foldable() && entries[run].Operation == entries[i].Operation {
			run++
		}
		if run == i {
			rows = append(rows, singleChangeRow(entries[i]))
			i++
			continue
		}
		if run-i == 1 {
			rows = append(rows, singleChangeRow(entries[i]))
		} else {
			rows = append(rows, collapsedChangeRow(entries[i:run]))
			collapsed += run - i
		}
		i = run
	}
	return rows, collapsed
}

func singleChangeRow(e jenkins.ConfigChange) configHistoryRow {
	detail := e.Rename()
	if reason := e.Reason(); reason != "" {
		if detail != "" {
			detail += "  "
		}
		detail += collapseWS(output.StripControl(reason))
	}
	return configHistoryRow{
		When:      e.Timestamp(),
		Operation: e.Operation,
		User:      e.Who(),
		Detail:    detail,
	}
}

// collapsedChangeRow summarizes a run of automated writes. The plugin sorts
// newest first, but the endpoints are chosen by parsed time rather than by
// position so an unsorted payload cannot print the range backwards.
func collapsedChangeRow(run []jenkins.ConfigChange) configHistoryRow {
	oldest, newest := run[len(run)-1], run[0]
	if a, aok := oldest.When(); aok {
		if b, bok := newest.When(); bok && a.After(b) {
			oldest, newest = newest, oldest
		}
	}
	return configHistoryRow{
		When:      oldest.Timestamp() + " → " + newest.Timestamp(),
		Operation: fmt.Sprintf("%d writes", len(run)),
		User:      newest.Who(),
		Detail:    "automated, collapsed",
	}
}
