package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/authcheck"
	"github.com/ysmaoui/jkit/internal/authprune"
	"github.com/ysmaoui/jkit/internal/config"
	"github.com/ysmaoui/jkit/internal/output"
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove config keys proven redundant by a valid twin (dry run unless --yes)",
	Args:  cobra.NoArgs,
	RunE:  runPrune,
}

func init() {
	pruneCmd.Flags().Bool("yes", false, "Apply the removals, after copying config.yml to config.yml.bak")
	AuthCmd.AddCommand(pruneCmd)
}

type pruneResult struct {
	DryRun bool `json:"dry_run"`
	authprune.Plan
	Backup string `json:"backup,omitempty"`
}

func runPrune(cmd *cobra.Command, args []string) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("yes")
	res := pruneResult{DryRun: !apply}

	_, _ = fmt.Fprintf(os.Stderr, "Checking %d host(s)...\n", len(cfg.Hosts))
	res.Plan = authprune.Decide(cfg, authcheck.CheckAll(context.Background(), cfg, authcheck.Options{}))

	if apply && len(res.Removals) > 0 {
		res.Backup, err = authprune.Apply(cfg, res.Plan).ReplaceWithBackup(raw)
		if err != nil {
			return err
		}
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		if err := output.NewFormatter(os.Stdout, isJSON, tmpl).Output(res, nil); err != nil {
			return err
		}
	} else {
		writePruneText(os.Stdout, res.Plan)
	}

	switch {
	case len(res.Removals) == 0:
		_, _ = fmt.Fprintln(os.Stderr, "nothing to remove")
	case res.DryRun:
		_, _ = fmt.Fprintln(os.Stderr, "dry run: re-run with --yes to apply")
	default:
		_, _ = fmt.Fprintf(os.Stderr, "removed %d key(s), backup in %s\n", len(res.Removals), res.Backup)
	}
	return nil
}

func writePruneText(w io.Writer, p authprune.Plan) {
	for _, r := range p.Removals {
		_, _ = fmt.Fprintf(w, "remove %s  (%s)\n", r.Host, r.Reason)
	}
	for _, m := range p.Moves {
		_, _ = fmt.Fprintf(w, "move   %s %s -> %s\n", m.Field, m.From, m.To)
	}
	for _, r := range p.Reports {
		if r.Suggestion == "" {
			_, _ = fmt.Fprintf(w, "note   %s  (%s)\n", r.Host, r.Reason)
		} else {
			_, _ = fmt.Fprintf(w, "keep   %s  (%s; run: %s)\n", r.Host, r.Reason, r.Suggestion)
		}
	}
}
