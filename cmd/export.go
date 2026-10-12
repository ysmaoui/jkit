package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
	appctx "github.com/ysmaoui/jkit/internal/context"
	"github.com/ysmaoui/jkit/internal/export"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

// exportTimeout replaces the 30s default of --timeout: one log is one HTTP
// response and the timeout covers its whole body, so a large log from a remote
// Jenkins outlasts 30s. A variable so tests can shorten it.
var exportTimeout = 10 * time.Minute

// Browsers slow down, then fail, on very large HTML files.
const (
	slowOpenBytes = 250 << 20
	noOpenBytes   = 500 << 20
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Save a build as a file that outlives the Jenkins instance",
}

var exportHTMLCmd = &cobra.Command{
	Use:   "html [job] [build#]",
	Short: "Write a pipeline run (stages, steps, logs) as one offline HTML file",
	Long: `Write a pipeline run as one self-contained HTML file: stage graph, steps,
every step log and the console. The file needs no network to open.

Works on a finished build and on a running one, which gives a snapshot of
what exists at call time. Inside a Jenkins build, with no arguments, it
exports the build that BUILD_URL names.

Unless --timeout is given, the HTTP timeout is 10m: each log is one response
and must download within it.`,
	Example: `  jkit export html my-app 42
  jkit export html my-app -o last.html
  jkit export html                      # inside a pipeline: this build
  jkit export html my-app 42 --max-log-bytes 20000000`,
	Args: cobra.MaximumNArgs(2),
	RunE: runExportHTML,
}

func init() {
	registerExportHTMLFlags(exportHTMLCmd)
	exportCmd.AddCommand(exportHTMLCmd)
	rootCmd.AddCommand(exportCmd)
}

// registerExportHTMLFlags is shared with the test harness, which resets flags.
func registerExportHTMLFlags(c *cobra.Command) {
	c.Flags().StringP("output", "o", "", "File to write (default <job>-<build#>.html in the current directory, / in the job path as _); - for stdout")
	c.Flags().Int64("max-log-bytes", 0, "Keep at most this many bytes of each log, half from the head and half from the tail (plain bytes, 0 = no limit). Shrinks the file, not the download")
}

type exportResult struct {
	Path         string `json:"path"`
	Bytes        int64  `json:"bytes"`
	Complete     bool   `json:"complete"`
	StepErrors   int    `json:"stepErrors"`
	ConsoleError bool   `json:"consoleError"`
}

func runExportHTML(cmd *cobra.Command, args []string) error {
	dest, _ := cmd.Flags().GetString("output")
	maxLog, _ := cmd.Flags().GetInt64("max-log-bytes")
	isJSON, _ := cmd.Flags().GetBool("json")
	if maxLog < 0 {
		return fmt.Errorf("--max-log-bytes must be 0 or more, got %d", maxLog)
	}
	tmpl, _ := cmd.Flags().GetString("format")
	if dest == "-" && (isJSON || tmpl != "") {
		return fmt.Errorf("--json and --format describe the written file on stdout, so they cannot be used with -o -")
	}
	if !cmd.Flags().Changed("timeout") {
		_ = cmd.Flags().Set("timeout", exportTimeout.String())
	}

	client, jobPath, buildNum, err := resolveExportTarget(cmd, args)
	if err != nil {
		return err
	}
	if buildNum == 0 {
		builds, err := client.GetBuilds(jobPath, 1)
		if err != nil {
			return err
		}
		if len(builds) == 0 {
			if hint := client.ContainerHint(jobPath); hint != nil {
				return hint
			}
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = builds[0].Number
	}

	if dest == "" {
		dest = fmt.Sprintf("%s-%d.html", strings.ReplaceAll(jobPath, "/", "_"), buildNum)
	}

	ctx, cancel := exportSignalContext()
	defer cancel()

	// Created before the download so an unwritable destination fails at once.
	var out *atomicFile
	if dest != "-" {
		if out, err = createAtomic(dest); err != nil {
			return err
		}
		defer out.abort()
	}

	progress := func(i, total int, n *export.Node, s *export.Step) {
		if s == nil {
			_, _ = fmt.Fprintln(os.Stderr, "fetching console log")
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "fetching step %d/%d: %s\n", i, total, stepLabel(n, s))
	}
	// The spool goes beside the output, where the output needs room anyway.
	// TMPDIR may be a small tmpfs, which the spool would fill or keep in RAM.
	spoolDir := ""
	if out != nil {
		spoolDir = filepath.Dir(dest)
	}
	snap, err := export.Collect(ctx, client, jobPath, buildNum, export.Options{MaxLogBytes: maxLog, Progress: progress, SpoolDir: spoolDir})
	if snap != nil {
		defer func() { _ = snap.Close() }()
	}
	if ctx.Err() != nil {
		return &jenkins.ExitError{Code: 130, Message: errInterrupted.Error()}
	}
	if err != nil {
		return err
	}

	var w io.Writer = os.Stdout
	if out != nil {
		w = out
	}
	cw := &countingWriter{w: w}
	if err := export.Render(cw, snap); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if ctx.Err() != nil {
		return &jenkins.ExitError{Code: 130, Message: errInterrupted.Error()}
	}
	if out != nil {
		if err := out.commit(); err != nil {
			return err
		}
	}

	res := exportResult{Path: dest, Bytes: cw.n, Complete: snap.Complete, ConsoleError: snap.ConsoleError != ""}
	steps := 0
	snap.EachStep(func(n *export.Node, s *export.Step) {
		steps++
		if s.Error != "" {
			res.StepErrors++
		}
	})
	_, _ = fmt.Fprintf(os.Stderr, "exported %d step logs and the console, %s\n", steps, formatBytes(cw.n))
	warnExport(os.Stderr, snap, cw.n)

	switch {
	case dest == "-":
		return nil
	case isJSON || tmpl != "":
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(res, nil)
	}
	_, _ = fmt.Fprintln(os.Stdout, dest)
	return nil
}

// exportSignalContext ends on Ctrl-C and on SIGTERM, which Jenkins sends to a
// step's processes when the build is aborted; either way the temp file must go.
// A variable so tests cancel without a signal.
var exportSignalContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// resolveExportTarget is resolveJobArgs, except that with no args it targets
// the build that BUILD_URL names: Jenkins sets it inside every build.
//
// The host is the longest configured host or JENKINS_URL that BUILD_URL lies
// under, as for a pasted URL. JENKINS_URL still counts when no configured host
// matches: it names the context path (https://x/jenkins/), so the missing
// credentials are reported for the root they must be stored under, not for
// https://x. --host overrides the host either way, for a Jenkins whose own URL
// differs from the one jkit knows it by.
func resolveExportTarget(cmd *cobra.Command, args []string) (*api.Client, string, int, error) {
	buildURL := os.Getenv("BUILD_URL")
	if len(args) > 0 || buildURL == "" {
		return resolveJobArgs(cmd, args, false)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, "", 0, err
	}
	parsed, err := parseBuildURL(buildURL, os.Getenv("JENKINS_URL"), cfg.HostURLs())
	if err != nil {
		return nil, "", 0, err
	}
	_, _ = fmt.Fprintf(os.Stderr, "exporting the build BUILD_URL names: %s #%d\n", parsed.JobPath, parsed.BuildNumber)
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		return resolveJobArgs(cmd, []string{parsed.JobPath, strconv.Itoa(parsed.BuildNumber)}, false)
	}
	client, err := clientFromURL(cfg, parsed.Host, clientOpts(cmd)...)
	if err != nil {
		return nil, "", 0, err
	}
	return client, withBranch(cmd, parsed.JobPath, true), parsed.BuildNumber, nil
}

// parseBuildURL parses buildURL, taking its Jenkins root from hosts and
// jenkinsURL.
func parseBuildURL(buildURL, jenkinsURL string, hosts []string) (*appctx.ParsedURL, error) {
	roots := append([]string{jenkinsURL}, hosts...)
	parsed, err := appctx.ParseJenkinsURLUnder(buildURL, roots)
	if err != nil {
		return nil, fmt.Errorf("BUILD_URL %q: %w", buildURL, err)
	}
	return parsed, nil
}

func warnExport(w io.Writer, snap *export.Snapshot, size int64) {
	if !snap.Complete {
		at := time.UnixMilli(snap.ExportedAtMillis).Format("2006-01-02 15:04:05 MST")
		_, _ = fmt.Fprintf(w, "warning: run still in progress; this is a snapshot at %s\n", at)
	}
	snap.EachStep(func(n *export.Node, s *export.Step) {
		if s.Error != "" {
			_, _ = fmt.Fprintf(w, "warning: step %s: %s\n", stepLabel(n, s), s.Error)
		}
	})
	if snap.ConsoleError != "" {
		_, _ = fmt.Fprintf(w, "warning: console log: %s\n", snap.ConsoleError)
	}
	switch {
	case size > noOpenBytes:
		_, _ = fmt.Fprintln(w, "warning: browsers cannot open files over about 500 MB; re-export with --max-log-bytes")
	case size > slowOpenBytes:
		_, _ = fmt.Fprintln(w, "warning: browsers take over 25s to open files this large")
	}
}

// stepLabel names a step as "stage › step". An sh step has no title, and its
// name is the whole script.
func stepLabel(n *export.Node, s *export.Step) string {
	name := s.Title
	if name == "" {
		name = s.Name
	}
	name = collapseWS(name)
	if r := []rune(name); len(r) > 60 {
		name = string(r[:59]) + "…"
	}
	return n.Name + " › " + name
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// atomicFile is written beside its destination and renamed onto it by
// commit, so a failed or interrupted export leaves no partial file.
type atomicFile struct {
	dest string
	f    *os.File
	buf  *bufio.Writer
	done bool
}

func createAtomic(dest string) (*atomicFile, error) {
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", dest, err)
	}
	return &atomicFile{dest: dest, f: f, buf: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (a *atomicFile) Write(p []byte) (int, error) { return a.buf.Write(p) }

func (a *atomicFile) commit() error {
	err := a.buf.Flush()
	if err == nil {
		// CreateTemp makes the file 0600, but the export is meant to be
		// shared; fixed 0644 because reading the umask is not portable.
		err = a.f.Chmod(0o644)
	}
	if cerr := a.f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(a.f.Name(), a.dest)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", a.dest, err)
	}
	a.done = true
	return nil
}

// abort removes the temp file unless commit renamed it.
func (a *atomicFile) abort() {
	if a.done {
		return
	}
	_ = a.f.Close()
	_ = os.Remove(a.f.Name())
}
