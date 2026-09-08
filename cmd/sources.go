package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var sourcesCmd = &cobra.Command{
	Use:   "sources [job] [build#]",
	Short: "Show which code a build ran: pipeline revision, checkouts, library versions",
	Long: `Report the code one build actually executed: the revision the pipeline was read
at, every repository the git plugin checked out with its commit, and every
shared library with the ref it asked for alongside the commit that ref resolved
to.

This is the answer to "same commit, worked yesterday, fails today". A library
loaded @develop is not the same code from one build to the next, and neither
the job configuration nor the changelog records that it moved.

Jenkins records a library's requested ref and a checkout's commit in two
different actions with nothing linking them, so a commit is reported only where
it cannot be attributed to the wrong library. Everything else prints as
"SHA not resolvable" with the reason, next to the raw checkout list it was
weighed against.`,
	Example: `  jkit sources my-app
  jkit sources my-app 42
  jkit sources my-app 42 --json`,
	Args: cobra.MaximumNArgs(2),
	RunE: runSources,
}

// registerSourcesFlags is called from init and from the test harness, which
// resets every subcommand's flags between runs.
func registerSourcesFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("show-secrets", false, "Do not mask credentials embedded in checkout urls")
}

func init() {
	registerSourcesFlags(sourcesCmd)
	rootCmd.AddCommand(sourcesCmd)
}

func runSources(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}
	if buildNum <= 0 {
		job, err := client.GetJob(jobPath)
		if err != nil {
			return err
		}
		if job.LastBuild == nil {
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = job.LastBuild.Number
	}

	src, err := client.GetBuildSources(jobPath, buildNum)
	if err != nil {
		return err
	}
	if showSecrets, _ := cmd.Flags().GetBool("show-secrets"); !showSecrets {
		redactSourceRemotes(src)
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(src, nil)
	}

	printBuildSources(os.Stdout, src)
	return nil
}

// redactSourceRemotes masks credentials embedded in checkout urls. It runs
// before any output path so text, --json and --format are covered by one pass.
func redactSourceRemotes(src *jenkins.BuildSources) {
	for i := range src.Checkouts {
		for j, u := range src.Checkouts[i].RemoteURLs {
			src.Checkouts[i].RemoteURLs[j] = output.RedactURLCredentials(u)
		}
	}
	for i := range src.Libraries {
		src.Libraries[i].RemoteURL = output.RedactURLCredentials(src.Libraries[i].RemoteURL)
	}
}

func printBuildSources(w io.Writer, src *jenkins.BuildSources) {
	kv(w, "Job", src.Job)
	kv(w, "Build", fmt.Sprintf("#%d", src.Build))

	printPipelineRevisions(w, src.PipelineRevisions)
	printSharedLibraries(w, src)
	printGitCheckouts(w, src.Checkouts)
	printSourcesNotes(w, src)
}

func printPipelineRevisions(w io.Writer, revs []jenkins.PipelineRevision) {
	section(w, "Pipeline revision")
	if len(revs) == 0 {
		_, _ = fmt.Fprintln(w, "  none recorded: this build was not created from a branch source.")
		_, _ = fmt.Fprintln(w, "  A job whose Jenkinsfile comes from its own SCM checkout records that below instead.")
		return
	}
	for _, r := range revs {
		if r.Hash == "" {
			// A non-git or pull-request revision exports its own fields rather
			// than a plain hash, so an empty one is a gap in this report, not a
			// build without a revision.
			_, _ = fmt.Fprintf(w, "  recorded, but %s exports no commit hash to this query\n", r.RevisionClass)
			continue
		}
		kv2(w, "Revision", r.Hash)
		kv2(w, "Recorded by", r.RevisionClass)
	}
}

func printSharedLibraries(w io.Writer, src *jenkins.BuildSources) {
	if len(src.Libraries) == 0 {
		section(w, "Shared libraries")
		if src.LibrariesActionPresent {
			_, _ = fmt.Fprintln(w, "  the build carries a LibrariesAction that lists no library.")
			return
		}
		_, _ = fmt.Fprintln(w, "  none: the build carries no LibrariesAction, so it loaded no shared library")
		_, _ = fmt.Fprintln(w, "  — unless workflow-cps-global-lib is not installed, which a build record cannot show.")
		return
	}

	section(w, fmt.Sprintf("Shared libraries (%d)", len(src.Libraries)))
	for _, l := range src.Libraries {
		_, _ = fmt.Fprintf(w, "  %s @ %s  (%s)\n", l.Name, l.Version, trustLabel(l.Trusted))
		if l.SHA1 == "" {
			_, _ = fmt.Fprintf(w, "      SHA not resolvable: %s\n", l.Reason)
			continue
		}
		_, _ = fmt.Fprintf(w, "      commit  %s  (%s)\n", l.SHA1, evidenceLabel(l.Resolution))
		if l.RemoteURL != "" {
			_, _ = fmt.Fprintf(w, "      repo    %s\n", l.RemoteURL)
		}
	}
}

// evidenceLabel says what the commit above it rests on. A commit only ever
// reaches the report two ways, and the reader has to be able to tell which.
func evidenceLabel(resolution string) string {
	if resolution == jenkins.LibraryPinned {
		return "the requested version is itself a commit id"
	}
	return "matched to a checkout by branch name"
}

// trustLabel names whether the library ran outside the Groovy sandbox. A
// trusted library is loaded with full script approval, so which one it was
// matters as much as which commit it was at.
func trustLabel(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

func printGitCheckouts(w io.Writer, checkouts []jenkins.GitCheckout) {
	if len(checkouts) == 0 {
		section(w, "Git checkouts")
		_, _ = fmt.Fprintln(w, "  none: the build carries no BuildData, so the git plugin checked nothing out.")
		_, _ = fmt.Fprintln(w, "  A pipeline that clones by hand, or uses another SCM, records nothing here.")
		return
	}

	section(w, fmt.Sprintf("Git checkouts (%d, last built revision only)", len(checkouts)))
	for _, c := range checkouts {
		remote := c.Remote()
		if remote == "" {
			remote = "(no remote url recorded)"
		}
		_, _ = fmt.Fprintf(w, "  %s\n", remote)
		line := c.SHA1
		if line == "" {
			line = "(no revision recorded)"
		}
		if len(c.Branches) > 0 {
			line += "  branch " + strings.Join(c.Branches, ", ")
		}
		if c.Library != "" {
			line += "  -> shared library " + c.Library
		}
		_, _ = fmt.Fprintf(w, "      %s\n", line)
	}
}

func printSourcesNotes(w io.Writer, src *jenkins.BuildSources) {
	var notes []string
	if moving := src.MovingLibraries(); moving > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d of %d libraries are requested by name rather than by commit, so rebuilding this job can run different library code without any change to the job or its repository.",
			moving, len(src.Libraries)))
	}
	if unresolved := countUnresolved(src.Libraries); unresolved > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d of %d libraries have no commit above. jkit reports one only where it cannot belong to another library; compare the checkouts by hand.",
			unresolved, len(src.Libraries)))
	}
	notes = append(notes, src.Warnings...)
	if len(notes) == 0 {
		return
	}
	section(w, "Notes")
	for _, n := range notes {
		_, _ = fmt.Fprintf(w, "  %s\n", n)
	}
}

func countUnresolved(libs []jenkins.SharedLibrary) int {
	n := 0
	for _, l := range libs {
		if l.Resolution == jenkins.LibraryUnresolved {
			n++
		}
	}
	return n
}
