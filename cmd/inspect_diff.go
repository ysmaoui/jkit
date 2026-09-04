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

// checkDiffFlags rejects the timestamp flags outside --diff and validates their
// format before anything is fetched. The server cannot help here: configOutput
// answers 200 with an empty body for a timestamp it does not hold, so a
// malformed one would otherwise come back as a missing revision.
func checkDiffFlags(cmd *cobra.Command, jobPath string, diff bool) error {
	from, _ := cmd.Flags().GetString("diff-from")
	to, _ := cmd.Flags().GetString("diff-to")
	if from == "" && to == "" {
		return nil
	}
	if !diff {
		return fmt.Errorf("--diff-from and --diff-to only apply to --diff, which compares two stored revisions of the job's config.xml")
	}
	if from == "" || to == "" {
		return fmt.Errorf("--diff-from and --diff-to name the two revisions to compare: pass both, or neither to compare the two most recent")
	}
	for _, ts := range []string{from, to} {
		if _, ok := jenkins.ParseConfigTimestamp(ts); !ok {
			return fmt.Errorf("%q is not a config history timestamp: they look like %s, and jkit inspect %s --history lists the ones this job has",
				ts, jenkins.ConfigTimestampLayout, jobPath)
		}
	}
	return nil
}

func runInspectDiff(cmd *cobra.Command, client *api.Client, jobPath string) error {
	from, _ := cmd.Flags().GetString("diff-from")
	to, _ := cmd.Flags().GetString("diff-to")
	if from == "" {
		var err error
		if from, to, err = latestRevisionPair(client, jobPath); err != nil {
			return err
		}
	}
	from, to = olderFirst(from, to)

	oldConfig, err := client.GetJobConfigRevision(jobPath, from)
	if err != nil {
		return err
	}
	newConfig, err := client.GetJobConfigRevision(jobPath, to)
	if err != nil {
		return err
	}
	diff := jenkins.DiffConfigRevisions(jobPath, from, to, oldConfig, newConfig)

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(diff, nil)
	}
	return printConfigDiff(os.Stdout, os.Stderr, diff)
}

// latestRevisionPair picks the two most recent revisions, the pair a reader
// asking "what changed?" wants by default. The plugin lists newest first.
func latestRevisionPair(client *api.Client, jobPath string) (string, string, error) {
	entries, err := client.GetJobConfigHistory(jobPath)
	if err != nil {
		return "", "", err
	}
	switch len(entries) {
	case 0:
		return "", "", fmt.Errorf("no config history for %s to diff — either nothing has changed since the plugin started recording, or you cannot see it: without the Job/Configure or Job/ExtendedRead permission the plugin returns an empty list rather than refusing", jobPath)
	case 1:
		return "", "", fmt.Errorf("%s has one recorded revision, from %s, so there is nothing to compare it against: a diff needs two", jobPath, entries[0].Timestamp())
	}
	return entries[1].Date, entries[0].Date, nil
}

// olderFirst puts the older revision on the left. The plugin's own doDiffFiles
// swaps its arguments the same way, so a pair given in either order reads the
// same in jkit as it does in the Jenkins UI.
func olderFirst(from, to string) (string, string) {
	a, aok := jenkins.ParseConfigTimestamp(from)
	b, bok := jenkins.ParseConfigTimestamp(to)
	if aok && bok && a.After(b) {
		return to, from
	}
	return from, to
}

// printConfigDiff writes the unified diff to w and everything that is not part
// of it to warn, so the diff can be piped without commentary in the stream.
func printConfigDiff(w, warn io.Writer, d *jenkins.ConfigRevisionDiff) error {
	if len(d.Hunks) == 0 {
		_, _ = fmt.Fprintf(warn, "No change: %s stored the same config.xml at %s and %s.\n", d.Job, d.From, d.To)
		return nil
	}

	_, _ = fmt.Fprintf(w, "--- %s @ %s\n", d.Job, d.From)
	_, _ = fmt.Fprintf(w, "+++ %s @ %s\n", d.Job, d.To)
	for _, h := range d.Hunks {
		_, _ = fmt.Fprintln(w, h.Header())
		for _, line := range h.Lines {
			_, _ = fmt.Fprintln(w, line.Op+output.StripControl(line.Text))
		}
	}

	if d.MaskedOnly {
		_, _ = fmt.Fprintf(warn, "\nThe two revisions are identical apart from values Jenkins hides from you, so this is not evidence that the job was reconfigured: without Job/Configure the controller masks secrets on the way out, and it re-encrypts stored ones whenever the job is saved, which changes the bytes and not the setting.\n")
	}
	return nil
}
