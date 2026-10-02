package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
	"github.com/ysmaoui/jkit/internal/waiter"
)

var stagesCmd = &cobra.Command{
	Use:   "stages [job] [build#]",
	Short: "List pipeline stages with IDs and qualified paths",
	Long: `List the stages of a pipeline build, including each stage's node ID and a
qualified path that disambiguates duplicate names across parallel branches
(e.g. "RemoteExec/Run Bazel Build"). Feed a path or an ID to "jkit log --stage"
or "jkit wait --stage". The DURATION of a running stage is the time so far.`,
	Example: `  jkit stages my-app
  jkit stages my-app 42
  jkit stages my-app 42 --json`,
	Args: cobra.MaximumNArgs(2),
	RunE: runStages,
}

func init() {
	rootCmd.AddCommand(stagesCmd)
}

// stageInfo is the JSON/output shape for a single stage, adding the computed
// qualified path to the raw stage fields.
type stageInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	DurationMillis int64  `json:"durationMillis"`
	// ElapsedMillis is the time so far of a running, paused or queued stage.
	// Absent once the stage has finished.
	ElapsedMillis int64  `json:"elapsedMillis,omitempty"`
	Agent         string `json:"agent,omitempty"`
	// elapsed backs the text DURATION column.
	elapsed time.Duration
}

func runStages(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
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

	stages, err := client.GetPipelineStages(jobPath, buildNum)
	if err != nil {
		return err
	}
	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if stages == nil {
		build, pending, err := waiter.ReadBuild(context.Background(), client, jobPath, buildNum, false)
		if err != nil {
			return err
		}
		if build == nil {
			state := "queued"
			if pending == waiter.Starting {
				state = "starting"
			}
			_, _ = fmt.Fprintf(os.Stderr, "build #%d is %s\n", buildNum, state)
			if isJSON || tmpl != "" {
				return f.Output([]stageInfo{}, nil)
			}
			return nil
		}
		if hint := client.ContainerHint(jobPath); hint != nil {
			return hint
		}
		return fmt.Errorf("no stages found — pipeline graph view or blue ocean plugin required")
	}

	// An empty list is stage data: the pipeline has not entered a stage.
	if len(stages) == 0 {
		build, err := client.GetBuild(jobPath, buildNum)
		if err != nil {
			return err
		}
		if !build.Building {
			return fmt.Errorf("no stages — build #%d finished %s without entering a stage", buildNum, build.Result)
		}
		_, _ = fmt.Fprintf(os.Stderr, "no stages yet (build #%d is running)\n", buildNum)
		if isJSON || tmpl != "" {
			return f.Output([]stageInfo{}, nil)
		}
		return nil
	}

	paths := jenkins.QualifiedStagePaths(stages)
	infos := make([]stageInfo, len(stages))
	now := clock()
	for i, s := range stages {
		elapsed := s.Elapsed(now)
		running, _ := s.RunningElapsed(now)
		infos[i] = stageInfo{
			ID:             s.ID,
			Name:           s.Name,
			Path:           paths[s.ID],
			Type:           s.Type,
			Status:         s.Status,
			DurationMillis: s.DurationMillis,
			ElapsedMillis:  running.Milliseconds(),
			elapsed:        elapsed,
			Agent:          s.Agent,
		}
	}

	warnIfNoAgents(os.Stderr, infos)

	if isJSON || tmpl != "" {
		return f.Output(infos, nil)
	}

	items := make([]any, len(infos))
	for i := range infos {
		items[i] = infos[i]
	}

	columns := []output.Column{
		{Header: "ID", Field: func(v any) string { return v.(stageInfo).ID }},
		{Header: "STAGE", Field: func(v any) string { return v.(stageInfo).Path }},
		{Header: "TYPE", Field: func(v any) string {
			if t := v.(stageInfo).Type; t != "" {
				return t
			}
			return "-"
		}},
		{Header: "STATUS", Field: func(v any) string {
			s := v.(stageInfo).Status
			if s == "" {
				return "-"
			}
			return output.ColorStatus(s)
		}},
		{Header: "DURATION", Field: func(v any) string {
			return formatDuration(v.(stageInfo).elapsed)
		}},
		{Header: "AGENT", Field: func(v any) string {
			if a := v.(stageInfo).Agent; a != "" {
				return a
			}
			return "-"
		}},
	}

	return f.Output(items, columns)
}

// warnIfNoAgents explains an AGENT column that is entirely "-". Blue Ocean's
// /nodes/ carries no agent field at all, so the fallback source produces the
// same output as a pipeline that genuinely never entered a node block. Without
// this the reader cannot tell the two apart.
func warnIfNoAgents(w io.Writer, infos []stageInfo) {
	for _, s := range infos {
		if s.Agent != "" {
			return
		}
	}
	_, _ = fmt.Fprintln(w, "note: no stage reports an agent — either the pipeline declared no node, "+
		"or the stage list came from Blue Ocean, which does not report one (see --pipeline-source)")
}
