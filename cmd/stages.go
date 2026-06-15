package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var stagesCmd = &cobra.Command{
	Use:   "stages [job] [build#]",
	Short: "List pipeline stages with IDs and qualified paths",
	Long: `List the stages of a pipeline build, including each stage's node ID and a
qualified path that disambiguates duplicate names across parallel branches
(e.g. "RemoteExec/Run Bazel Build"). Feed a path to "jkit log --stage" or an
ID to "jkit log --stage-id".`,
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
	if len(stages) == 0 {
		if hint := client.ContainerHint(jobPath); hint != nil {
			return hint
		}
		return fmt.Errorf("no stages found — pipeline graph view or blue ocean plugin required")
	}

	paths := jenkins.QualifiedStagePaths(stages)
	infos := make([]stageInfo, len(stages))
	for i, s := range stages {
		infos[i] = stageInfo{
			ID:             s.ID,
			Name:           s.Name,
			Path:           paths[s.ID],
			Type:           s.Type,
			Status:         s.Status,
			DurationMillis: s.DurationMillis,
		}
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

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
			return formatDuration(time.Duration(v.(stageInfo).DurationMillis) * time.Millisecond)
		}},
	}

	return f.Output(items, columns)
}
