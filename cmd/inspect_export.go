package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
)

// checkExportFlags rejects the --recursive combinations cobra cannot express.
// -o with --recursive is a mutual exclusion declared in registerInspectFlags;
// what is left is the "this flag needs that one" direction, which a flag group
// cannot state and which would otherwise be accepted and do nothing.
func checkExportFlags(cmd *cobra.Command) error {
	xml, _ := cmd.Flags().GetBool("xml")
	recursive, _ := cmd.Flags().GetBool("recursive")
	dir, _ := cmd.Flags().GetString("out-dir")

	if recursive && !xml {
		return fmt.Errorf("--recursive exports raw config.xml files, so it needs --xml; the decoded summary covers one job")
	}
	if dir != "" && !xml {
		return fmt.Errorf("-d is where --xml --recursive writes the exported tree, so it needs both")
	}
	if recursive && dir == "" {
		return fmt.Errorf("--recursive writes one config.xml per job, so it needs -d DIR to write them into")
	}
	if dir != "" && !recursive {
		return fmt.Errorf("-d holds a tree of config.xml files, so it needs --recursive; one job's config.xml goes to stdout, or to -o FILE")
	}
	return nil
}

// exportJobConfigs writes config.xml for jobPath and for every job below it
// into a directory tree under dir that mirrors the folder layout, one directory
// per job named by the job name verbatim.
//
// Verbatim matters for multibranch branch jobs: Jenkins stores the branch
// "feature/build" as a job NAME of "feature%2Fbuild", so the encoded form is
// the real name. Written as-is it stays one directory entry and feeds straight
// back to jkit inspect; decoding it into nested directories would change the
// tree shape and make it indistinguishable from a folder "feature" holding a
// job "build".
func exportJobConfigs(client *api.Client, jobPath, dir string, warn io.Writer) error {
	tree, err := client.ListJobTree(jobPath)
	if err != nil {
		return err
	}
	e := &configExport{client: client}
	e.write(jobPath, dir)
	e.walk(jobPath, dir, tree)
	return e.report(dir, warn)
}

type configExport struct {
	client  *api.Client
	files   int
	bytes   int
	skipped []string
}

func (e *configExport) walk(jobPath, dir string, jobs []jenkins.Job) {
	for _, j := range jobs {
		if err := checkJobName(j.Name); err != nil {
			e.skip(jobPath+"/"+j.Name, fmt.Errorf("%w, so it and anything under it was not written", err))
			continue
		}
		childPath := strings.TrimRight(jobPath, "/") + "/" + j.Name
		childDir := filepath.Join(dir, j.Name)
		e.write(childPath, childDir)
		e.walk(childPath, childDir, j.Jobs)
	}
}

func (e *configExport) write(jobPath, dir string) {
	raw, err := e.client.GetJobConfigXML(jobPath)
	if err != nil {
		e.skip(jobPath, err)
		return
	}
	if err := os.MkdirAll(dir, configDirMode); err != nil {
		e.skip(jobPath, err)
		return
	}
	dest := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(dest, raw, configFileMode); err != nil {
		e.skip(jobPath, fmt.Errorf("writing %s: %w", dest, err))
		return
	}
	e.files++
	e.bytes += len(raw)
}

// skip records a failure instead of returning it: an export of 25 jobs that
// abandons the remaining 24 over one unreadable job is worth less than a
// partial one that names what is missing.
func (e *configExport) skip(jobPath string, err error) {
	e.skipped = append(e.skipped, fmt.Sprintf("%s: %v", strings.Trim(jobPath, "/"), err))
}

// report names the count and the destination because the files are unredacted
// config.xml: a bulk export can leave credentials from SCM urls sitting in a
// directory, and the person who ran it should not have to guess how many or
// where. A partial export exits non-zero so a script does not read it as whole.
func (e *configExport) report(dir string, w io.Writer) error {
	_, _ = fmt.Fprintf(w, "Wrote %d config.xml files (%d bytes) to %s\n", e.files, e.bytes, dir)
	_, _ = fmt.Fprintf(w, "They are unredacted, exactly as Jenkins stores them, so any credential embedded in an SCM url is now on disk there.\n")
	if len(e.skipped) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(w, "\nSkipped %d:\n", len(e.skipped))
	for _, s := range e.skipped {
		_, _ = fmt.Fprintf(w, "  %s\n", s)
	}
	return &jenkins.ExitError{Code: 1}
}

// checkJobName rejects a job name that cannot be a single directory entry under
// the output directory. Job names are user data on the Jenkins side and this
// turns them into filesystem paths, so "..", a separator or an empty name would
// write outside -d.
//
// Rejecting beats rewriting: the directory name is the job name, and a rewrite
// would produce a tree that no longer maps back to the jobs it came from. A
// branch job's slash arrives percent-encoded as part of the name, so a raw
// separator here is not a nested job.
func checkJobName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("the job has no name")
	case name == "." || name == "..":
		return fmt.Errorf("the name %q would not stay inside the output directory", name)
	case strings.ContainsAny(name, `/\`+"\x00"):
		return fmt.Errorf("the name %q contains a path separator, which would write outside the output directory", name)
	}
	return nil
}
