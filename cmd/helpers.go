package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/api"
	"github.com/ysmaoui/jk/internal/config"
	appctx "github.com/ysmaoui/jk/internal/context"
	"github.com/ysmaoui/jk/internal/output"
)

// newFetchLog creates a FetchLogFunc from an API client.
func newFetchLog(client *api.Client) output.FetchLogFunc {
	return func(jp string, num int, start int64) (string, int64, bool, error) {
		chunk, err := client.GetBuildLog(jp, num, start)
		if err != nil {
			return "", 0, false, err
		}
		return output.SanitizeLog(chunk.Text), chunk.Offset, chunk.HasMore, nil
	}
}

// resolveJobArgs extracts client, job path, and optional build number from command arguments.
// If the first argument is a Jenkins URL, it parses host/job/build from it and looks up credentials.
// Otherwise falls back to positional args and context resolution.
func resolveJobArgs(cmd *cobra.Command, args []string, needBuild bool) (*api.Client, string, int, error) {
	if len(args) > 0 && (strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://")) {
		parsed, err := appctx.ParseJenkinsURL(args[0])
		if err != nil {
			return nil, "", 0, err
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, "", 0, err
		}
		client, err := clientFromURL(cfg, parsed.Host, clientOpts(cmd)...)
		if err != nil {
			return nil, "", 0, err
		}
		return client, parsed.JobPath, parsed.BuildNumber, nil
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
			_, _ = fmt.Fprintf(os.Stderr, "warning: guessed job from git remote: %s — use .jk.yml or pass job arg if incorrect\n", jobPath)
		case "dirname":
			_, _ = fmt.Fprintf(os.Stderr, "warning: guessed job from directory name: %s — use .jk.yml or pass job arg if incorrect\n", jobPath)
		}
	}
	return client, jobPath, buildNum, nil
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
