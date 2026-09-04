package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect [job]",
	Short: "Show a job's definition: Jenkinsfile, SCM source, discovery rules",
	Long: `Read a job's config.xml and report which Jenkinsfile runs, the repository and
credentials behind it, which branches and PRs the indexing discovers, when a
discovered head actually builds, and how long builds are kept. /api/json answers
none of this; reading config.xml needs the Job/ExtendedRead permission.

--history answers a different question, "it worked last week, what changed?":
it lists who edited the job's configuration and when, from the JobConfigHistory
plugin. --diff answers the follow-up, "what did that change do?": it prints a
unified diff of the stored config.xml between two of those revisions.`,
	Example: `  jkit inspect my-app
  jkit inspect team/backend/my-service
  jkit inspect https://jenkins.example.com/job/team/job/my-app/
  jkit inspect my-app --json
  jkit inspect my-app --history
  jkit inspect my-app --diff`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInspect,
}

func init() {
	registerInspectFlags(inspectCmd)
	rootCmd.AddCommand(inspectCmd)
}

// registerInspectFlags declares the whole flag surface in one place. The test
// harness resets subcommand flags and rebuilds them, so a surface split across
// two call sites drifts silently until a flag turns up missing under test.
//
// The mutual exclusions matter because inspect picks its mode with a flag
// rather than a subcommand: a subcommand would shadow any job actually named
// after it, and the job name is user data. The cost of that choice is that each
// mode carries flags the others have no use for, so the relationships are
// declared rather than left to quietly do nothing.
func registerInspectFlags(c *cobra.Command) {
	c.Flags().Bool("show-secrets", false, "Do not mask credentials embedded in SCM urls")
	c.Flags().Bool("history", false, "List config changes (who changed the job and when) instead of its definition")
	c.Flags().Bool("show-system", false, "With --history, list automated SYSTEM writes instead of collapsing them")
	c.Flags().Bool("xml", false, "Print the raw config.xml instead of the decoded summary")
	c.Flags().StringP("output", "o", "", "With --xml, write to this file instead of stdout")
	c.Flags().Bool("diff", false, "Show what changed in the config.xml between two recorded revisions")
	c.Flags().String("diff-from", "", "With --diff, the older revision, as a --history timestamp (2006-01-02_15-04-05)")
	c.Flags().String("diff-to", "", "With --diff, the newer revision, as a --history timestamp")
	c.MarkFlagsMutuallyExclusive("history", "show-secrets")
	c.MarkFlagsMutuallyExclusive("show-system", "show-secrets")
	c.MarkFlagsMutuallyExclusive("xml", "history")
	c.MarkFlagsMutuallyExclusive("xml", "show-system")
	c.MarkFlagsMutuallyExclusive("xml", "show-secrets")
	c.MarkFlagsMutuallyExclusive("diff", "xml")
	c.MarkFlagsMutuallyExclusive("diff", "history")
	c.MarkFlagsMutuallyExclusive("diff", "show-system")
	c.MarkFlagsMutuallyExclusive("diff", "show-secrets")
}

func runInspect(cmd *cobra.Command, args []string) error {
	client, jobPath, _, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	if xml, _ := cmd.Flags().GetBool("xml"); xml {
		return writeJobConfigXML(cmd, client, jobPath)
	}
	if out, _ := cmd.Flags().GetString("output"); out != "" {
		return fmt.Errorf("-o writes the raw config.xml, so it needs --xml; the decoded summary goes to stdout")
	}

	history, _ := cmd.Flags().GetBool("history")
	diff, _ := cmd.Flags().GetBool("diff")
	// Cobra can express mutual exclusion but not "this flag needs that one",
	// and a flag that silently does nothing is worse than one that objects.
	if showSystem, _ := cmd.Flags().GetBool("show-system"); showSystem && !history {
		return fmt.Errorf("--show-system only applies to --history, which lists a job's config changes")
	}
	if err := checkDiffFlags(cmd, jobPath, diff); err != nil {
		return err
	}
	if history {
		return runInspectHistory(cmd, client, jobPath)
	}
	if diff {
		return runInspectDiff(cmd, client, jobPath)
	}

	def, err := client.GetJobDefinition(jobPath)
	if err != nil {
		return err
	}

	if showSecrets, _ := cmd.Flags().GetBool("show-secrets"); !showSecrets {
		redactRemotes(def)
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(def, nil)
	}

	printJobDefinition(os.Stdout, def)
	return nil
}

func printJobDefinition(w io.Writer, def *jenkins.JobDefinition) {
	kv(w, "Job", def.JobPath)
	kv(w, "Type", def.Kind)
	kv(w, "Class", def.Class)
	if def.DisplayName != "" && def.DisplayName != def.JobPath {
		kv(w, "Name", def.DisplayName)
	}
	if def.Disabled != nil {
		state := "enabled"
		if *def.Disabled {
			state = "DISABLED, it will not build"
		}
		kv(w, "State", state)
	}
	if def.Description != "" {
		kv(w, "Description", truncate(collapseWS(def.Description), 100))
	}
	if def.Parent != "" {
		kv(w, "Branch of", def.Parent)
	}

	printScript(w, def)
	for i := range def.Sources {
		printSource(w, def.Sources[i], i, len(def.Sources))
	}
	if def.Parent != "" {
		_, _ = fmt.Fprintf(w, "\nDiscovery rules live on the parent: jkit inspect %s\n", def.Parent)
	}
	printTriggers(w, def.Triggers)
	printRetention(w, def.Retention)
	printCoverageNote(w, def)
}

// printCoverageNote states what the report does not cover, so an empty one is
// never mistaken for "nothing is configured". A container has no definition of
// its own; every other root builds something this tool did not read.
func printCoverageNote(w io.Writer, def *jenkins.JobDefinition) {
	switch {
	case def.Container && def.Script == nil && len(def.Sources) == 0:
		_, _ = fmt.Fprintf(w, "\nThis %s has no pipeline definition of its own; inspect a job inside it.\n", def.Kind)
	case !def.Container && def.Script == nil:
		_, _ = fmt.Fprintf(w, "\njkit does not read the build steps of a %s job; everything above is what it decoded from config.xml.\n", def.Kind)
	}
}

func printScript(w io.Writer, def *jenkins.JobDefinition) {
	s := def.Script
	if s == nil {
		return
	}
	section(w, "Pipeline script")
	switch s.Origin {
	case "inline":
		kv2(w, "Source", fmt.Sprintf("inline in the job config (%d lines), not in any repository", s.ScriptLines))
		if s.Sandbox != nil {
			kv2(w, "Sandbox", yesNo(*s.Sandbox))
		}
	case "scm":
		kv2(w, "Source", "checked out from SCM by the job itself")
		kv2(w, "Script path", s.ScriptPath)
		kv2(w, "Remote", s.Remote)
		kv2(w, "Branch", s.Branch)
		kv2(w, "Credentials", s.CredentialsID)
		if s.Lightweight != nil {
			kv2(w, "Lightweight", yesNo(*s.Lightweight))
		}
	case "branch-source":
		src := "the branch source below"
		if def.Parent != "" {
			src = "the branch source of " + def.Parent
		}
		kv2(w, "Source", "read from "+src+" at the revision being built")
		kv2(w, "Script path", s.ScriptPath)
		kv2(w, "Branch", s.Branch)
	default:
		kv2(w, "Source", notDecoded("definition "+s.Class))
		kv2(w, "Script path", s.ScriptPath)
	}
}

func printSource(w io.Writer, bs jenkins.BranchSource, i, total int) {
	title := "Repository"
	if bs.Kind == jenkins.SourceNavigator {
		title = "Organization navigator"
	}
	if total > 1 {
		title = fmt.Sprintf("%s %d of %d", title, i+1, total)
	}
	section(w, title)

	if bs.Source == nil {
		_, _ = fmt.Fprintln(w, "  no source element in config.xml")
		return
	}
	src := bs.Source
	kv2(w, "Provider", src.Provider)
	kv2(w, "API", src.APIURI)
	if src.RepoOwner != "" || src.Repository != "" {
		kv2(w, "Repo", strings.Trim(src.RepoOwner+"/"+src.Repository, "/"))
	}
	kv2(w, "Remote", src.Remote)
	kv2(w, "Credentials", src.CredentialsID)
	kv2(w, "Source id", src.ID)
	if src.Provider == "unknown" {
		kv2(w, "Class", src.Class)
	}

	// A branch child only records where it was cloned from; the rules that
	// decide what gets discovered and built live on its parent.
	if bs.Kind == jenkins.SourceCheckout {
		return
	}
	printTraits(w, bs)
	printBuildStrategies(w, bs.BuildStrategies)
}

func printTraits(w io.Writer, bs jenkins.BranchSource) {
	section(w, "Discovery (which heads indexing picks up)")
	if len(bs.Traits) == 0 {
		subject := "no branch, PR or tag"
		if bs.Kind == jenkins.SourceNavigator {
			subject = "no repository, and no branch, PR or tag inside one"
		}
		_, _ = fmt.Fprintf(w, "  no traits are configured, so this source discovers %s\n", subject)
		return
	}
	width := 0
	for _, t := range bs.Traits {
		if n := len(traitLabel(t)); n > width {
			width = n
		}
	}
	for _, t := range bs.Traits {
		_, _ = fmt.Fprintf(w, "%-*s  %s\n", width, traitLabel(t), traitDetail(t))
	}
}

// traitLabel flags an undecoded trait so it cannot be mistaken for one whose
// effect on discovery is fully reported.
func traitLabel(t jenkins.TraitInfo) string {
	if t.Decoded {
		return "  " + t.Name
	}
	return "! " + t.Name
}

func traitDetail(t jenkins.TraitInfo) string {
	if !t.Decoded {
		if t.RawValue != "" {
			return fmt.Sprintf("strategyId=%s is not a value this tool knows, class %s", t.RawValue, t.Class)
		}
		return notDecoded("class " + t.Class)
	}
	detail := t.Meaning
	if t.RawValue != "" && strings.HasSuffix(t.Class, "DiscoveryTrait") {
		detail = fmt.Sprintf("%s  [strategyId=%s]", t.Meaning, t.RawValue)
	}
	return detail + unreadNote(t.Unread)
}

func printBuildStrategies(w io.Writer, strategies []jenkins.BuildStrategy) {
	if len(strategies) == 0 {
		section(w, "Build strategies (when a discovered head builds)")
		_, _ = fmt.Fprintln(w, "  none are configured, so every discovered branch and PR builds when it changes, and tags never build")
		return
	}
	title := "Build strategies (when a discovered head builds)"
	if len(strategies) > 1 {
		title = "Build strategies (a discovered head builds when any one of these matches)"
	}
	section(w, title)
	var walk func(list []jenkins.BuildStrategy, depth int)
	walk = func(list []jenkins.BuildStrategy, depth int) {
		for _, s := range list {
			indent := strings.Repeat("  ", depth+1)
			detail := s.Meaning + unreadNote(s.Unread)
			if !s.Decoded {
				detail = notDecoded("class " + s.Class)
			}
			_, _ = fmt.Fprintf(w, "%s%s: %s\n", indent, s.Name, detail)
			for _, f := range s.Filters {
				line := f.Meaning + unreadNote(f.Unread)
				if !f.Decoded {
					line = notDecoded("filter class " + f.Class)
				}
				_, _ = fmt.Fprintf(w, "%s  %s\n", indent, line)
			}
			walk(s.Children, depth+1)
		}
	}
	walk(strategies, 0)
}

func printTriggers(w io.Writer, triggers []jenkins.TriggerInfo) {
	if len(triggers) == 0 {
		return
	}
	section(w, "Triggers")
	for _, t := range triggers {
		line := t.Name
		if t.Spec != "" {
			line += ": " + t.Spec
		}
		_, _ = fmt.Fprintf(w, "  %s\n", line)
		if t.Meaning != "" {
			_, _ = fmt.Fprintf(w, "    %s\n", t.Meaning)
		}
	}
}

func printRetention(w io.Writer, r *jenkins.RetentionPolicy) {
	if r == nil {
		return
	}
	section(w, "Build retention (from "+r.Source+")")
	kv2(w, "Keep builds for", keepDays(r.DaysToKeep))
	kv2(w, "Keep at most", keepCount(r.NumToKeep))
	kv2(w, "Keep artifacts for", keepDays(r.ArtifactDaysToKeep))
	kv2(w, "Keep artifacts of", keepCount(r.ArtifactNumToKeep))
}

// notDecoded is the single wording for anything the parser could not interpret.
// It has to read the same everywhere, because it is the signal that the report
// above it may be incomplete.
func notDecoded(what string) string { return "not decoded, " + what }

// unreadNote names configuration that a decoded entry carries and this tool
// ignored, so a complete-looking line does not hide an option that changes the
// answer.
func unreadNote(unread []string) string {
	if len(unread) == 0 {
		return ""
	}
	return "  [also set, not read: " + strings.Join(unread, ", ") + "]"
}

// keepDays and keepCount render a LogRotator bound. Jenkins writes -1 for "no
// limit" and the field is absent when never configured.
func keepDays(v *int) string {
	if v == nil || *v < 0 {
		return ""
	}
	return strconv.Itoa(*v) + " days"
}

func keepCount(v *int) string {
	if v == nil || *v < 0 {
		return ""
	}
	return strconv.Itoa(*v) + " builds"
}

func section(w io.Writer, title string) {
	_, _ = fmt.Fprintf(w, "\n%s\n", title)
}

func kv(w io.Writer, key, val string) {
	if val == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "%-13s %s\n", key+":", val)
}

func kv2(w io.Writer, key, val string) {
	if val == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "  %-19s %s\n", key+":", val)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// redactRemotes masks credentials embedded in SCM urls. It runs before any
// output path so text, --json and --format are covered by the same pass.
func redactRemotes(def *jenkins.JobDefinition) {
	if def.Script != nil {
		def.Script.Remote = output.RedactURLCredentials(def.Script.Remote)
	}
	for i := range def.Sources {
		src := def.Sources[i].Source
		if src == nil {
			continue
		}
		src.Remote = output.RedactURLCredentials(src.Remote)
		src.APIURI = output.RedactURLCredentials(src.APIURI)
	}
}

// writeJobConfigXML dumps config.xml unchanged. It is the escape hatch for
// fields the decoder does not model and the byte-exact source for migrating a
// job to code, so nothing here reformats or redacts: the caller asked for what
// Jenkins stores.
func writeJobConfigXML(cmd *cobra.Command, client *api.Client, jobPath string) error {
	raw, err := client.GetJobConfigXML(jobPath)
	if err != nil {
		return err
	}
	dest, _ := cmd.Flags().GetString("output")
	if dest == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Wrote %s (%d bytes)\n", dest, len(raw))
	return nil
}
