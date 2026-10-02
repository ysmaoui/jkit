package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
	appctx "github.com/ysmaoui/jkit/internal/context"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
	"github.com/ysmaoui/jkit/internal/waiter"
)

// streamLog follows a progressiveText log to w until it completes or ctx ends,
// telling errW once when the server leaves it unable to show more until then.
func streamLog(ctx context.Context, l *api.ProgressiveLog, w, errW io.Writer) error {
	lw := &sanitizingLineWriter{w: w}
	defer lw.Flush()
	noted := false
	fetch := func(ctx context.Context, w io.Writer) (bool, error) {
		more, err := l.Read(ctx, w)
		if err == nil && more && l.Stalled() && !noted {
			noted = true
			_, _ = fmt.Fprintln(errW, "note: this Jenkins sends a running log 10000 lines at a time without saying where they end; "+
				"the rest is shown when the log completes")
		}
		return more, err
	}
	return withConsoleTimeoutHint(output.NewLogStreamer(fetch, lw, consolePollInterval).Stream(ctx))
}

// reportPendingBuild tells why the stage endpoints 404, which a missing
// plugin, a missing build and a build still in the queue all produce. A
// queued or starting build is reported on errW as the console paths report it,
// and pending is true. A missing build is the typed not-found (or container)
// error.
func reportPendingBuild(client *api.Client, jobPath string, buildNum int, errW io.Writer) (pending bool, err error) {
	build, p, err := waiter.ReadBuild(context.Background(), client, jobPath, buildNum, false)
	if err != nil || build != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(errW, "build #%d is %s\n", buildNum, pendingState(p))
	return true, nil
}

// resolveJobArgs extracts client, job path, and optional build number from command arguments.
// If the first argument is a Jenkins URL, it parses host/job/build from it and looks up credentials.
// Otherwise falls back to positional args and context resolution.
func resolveJobArgs(cmd *cobra.Command, args []string, needBuild bool) (*api.Client, string, int, error) {
	return resolveTarget(cmd, args, needBuild, true)
}

// resolveContainerArgs resolves a target for a command whose subject is the
// container itself. Such a command gives --branch a meaning of its own, so the
// branch must not be appended to the job path: that would address a child job
// which has none of what the command reads.
func resolveContainerArgs(cmd *cobra.Command, args []string) (*api.Client, string, error) {
	client, jobPath, _, err := resolveTarget(cmd, args, false, false)
	return client, jobPath, err
}

func resolveTarget(cmd *cobra.Command, args []string, needBuild, applyBranch bool) (*api.Client, string, int, error) {
	if len(args) > 0 && (strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://")) {
		parsed, err := appctx.ParseJenkinsURL(args[0])
		if err != nil {
			return nil, "", 0, err
		}
		buildNum := parsed.BuildNumber
		if len(args) >= 2 {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return nil, "", 0, fmt.Errorf("invalid build number: %s", args[1])
			}
			if buildNum > 0 && buildNum != n {
				return nil, "", 0, fmt.Errorf("conflicting build numbers: #%d in the URL, #%d as an argument", buildNum, n)
			}
			buildNum = n
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, "", 0, err
		}
		client, err := clientFromURL(cfg, parsed.Host, clientOpts(cmd)...)
		if err != nil {
			return nil, "", 0, err
		}
		return client, withBranch(cmd, parsed.JobPath, applyBranch), buildNum, nil
	}

	client, _, err := clientFromCmd(cmd)
	if err != nil {
		return nil, "", 0, err
	}

	var jobPath string
	var buildNum int
	if len(args) >= 1 {
		jobPath = args[0]
	}
	if len(args) >= 2 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, "", 0, fmt.Errorf("invalid build number: %s", args[1])
		}
		buildNum = n
	}
	if jobPath == "" {
		resolved, err := appctx.Resolve()
		if err != nil {
			return nil, "", 0, err
		}
		jobPath = resolved.JobPath
		switch resolved.Source {
		case "git-remote":
			_, _ = fmt.Fprintf(os.Stderr, "warning: guessed job from git remote: %s — use .jkit.yml or pass job arg if incorrect\n", jobPath)
		case "dirname":
			_, _ = fmt.Fprintf(os.Stderr, "warning: guessed job from directory name: %s — use .jkit.yml or pass job arg if incorrect\n", jobPath)
		}
	}
	return client, withBranch(cmd, jobPath, applyBranch), buildNum, nil
}

// withBranch appends --branch to a job path as one segment. It applies to a URL
// target as well as a job path: a URL naming a multibranch job plus a branch is
// the same request as the job-path form, and accepting the flag there only to
// discard it is the accepted-and-ignored failure this tool refuses elsewhere.
//
// A URL that already ends in the branch is left alone, so pasting a branch
// child's URL and naming the same branch is not an error and does not append it
// twice.
func withBranch(cmd *cobra.Command, jobPath string, applyBranch bool) string {
	branch, _ := cmd.Flags().GetString("branch")
	if !applyBranch || branch == "" {
		return jobPath
	}
	// jkit prints branch jobs by their encoded job name, so a value copied from
	// its output is already the job name and must not be encoded again.
	seg := strings.Trim(branch, "/")
	if !jenkins.IsBranchJobName(seg) || !strings.Contains(seg, "%") {
		seg = jenkins.BranchJobName(seg)
	}
	trimmed := strings.TrimRight(jobPath, "/")
	if strings.HasSuffix(trimmed, "/"+seg) || trimmed == seg {
		return trimmed
	}
	return trimmed + "/" + seg
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "< 1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m < 60 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// Poll cadences for waitForBuildResult, as variables so tests drive the loop
// without real sleeps. The input check is far slower than the status poll
// because reading the InputAction makes the running pipeline's CPS thread
// answer; a paused build reports building:true exactly like a working one, so
// without that check --wait looks like a hang.
var (
	buildPollInterval = 2 * time.Second
	inputPollInterval = 30 * time.Second
)

// waitForBuildResult polls until the build finishes and maps its result to the
// process exit code. While polling it watches for input steps and announces
// each one once, so a build parked on a manual gate says so instead of looking
// stalled.
func waitForBuildResult(ctx context.Context, client *api.Client, jobPath string, buildNum int) error {
	deadline := time.After(2 * time.Hour)
	announced := map[string]bool{}
	nextInputCheck := time.Now().Add(inputPollInterval)

	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return fmt.Errorf("interrupted")
			case <-deadline:
				return fmt.Errorf("build timeout after 2h — check Jenkins for build #%d", buildNum)
			case <-time.After(buildPollInterval):
			}
		}

		build, err := client.GetBuild(jobPath, buildNum)
		if err != nil {
			return fmt.Errorf("polling build: %w", err)
		}
		if !build.Building {
			d := time.Duration(build.Duration) * time.Millisecond
			_, _ = fmt.Fprintf(os.Stderr, "Build #%d completed: %s (%s)\n", buildNum, build.Result, formatDuration(d))
			return buildResultError(build.Result)
		}

		if time.Now().After(nextInputCheck) {
			nextInputCheck = time.Now().Add(inputPollInterval)
			announcePendingInputs(client, jobPath, buildNum, announced)
		}
	}
}

func buildResultError(result string) error {
	switch result {
	case "SUCCESS":
		return nil
	case "FAILURE":
		return &jenkins.ExitError{Code: 1, Message: result}
	case "UNSTABLE":
		return &jenkins.ExitError{Code: 2, Message: result}
	case "ABORTED":
		return &jenkins.ExitError{Code: 3, Message: result}
	default:
		return &jenkins.ExitError{Code: 4, Message: fmt.Sprintf("unknown result: %s", result)}
	}
}

// announcePendingInputs is advisory: a failure to read the input state must not
// end a wait that is otherwise healthy, so errors are dropped.
func announcePendingInputs(client *api.Client, jobPath string, buildNum int, announced map[string]bool) {
	pending, err := client.GetPendingInputs(jobPath, buildNum)
	if err != nil {
		return
	}
	for _, in := range pending {
		if announced[in.ID] {
			continue
		}
		announced[in.ID] = true
		msg := in.Message
		if msg == "" {
			msg = "(no message)"
		}
		_, _ = fmt.Fprintf(os.Stderr, "Build #%d is paused for input: %s\n", buildNum, collapseWS(msg))
		_, _ = fmt.Fprintf(os.Stderr, "  approve: jkit input %s %d --approve --id %s\n", jobPath, buildNum, in.ID)
		_, _ = fmt.Fprintf(os.Stderr, "  deny:    jkit input %s %d --deny --id %s\n", jobPath, buildNum, in.ID)
	}
}
