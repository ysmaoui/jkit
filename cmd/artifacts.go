package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/api"
	"github.com/ysmaoui/jk/internal/jenkins"
	"github.com/ysmaoui/jk/internal/output"
)

var artifactsCmd = &cobra.Command{
	Use:   "artifacts [job] [build#]",
	Short: "List or download build artifacts",
	Example: `  jk artifacts my-app 42
  jk artifacts my-app 42 -d report.xml
  jk artifacts my-app 42 -d report.xml -o /tmp/report.xml`,
	Args: cobra.MaximumNArgs(2),
	RunE: runArtifacts,
}

func init() {
	artifactsCmd.Flags().StringP("download", "d", "", "Download artifact by filename")
	artifactsCmd.Flags().StringP("output", "o", "", "Output file path (default: current dir with original filename)")
	rootCmd.AddCommand(artifactsCmd)
}

func runArtifacts(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, false)
	if err != nil {
		return err
	}

	// Default to latest build
	if buildNum == 0 {
		builds, err := client.GetBuilds(jobPath, 1)
		if err != nil {
			return err
		}
		if len(builds) == 0 {
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = builds[0].Number
	}

	downloadName, _ := cmd.Flags().GetString("download")
	outputPath, _ := cmd.Flags().GetString("output")

	if downloadName != "" {
		return downloadArtifact(client, jobPath, buildNum, downloadName, outputPath)
	}

	// List artifacts
	artifacts, err := client.GetArtifacts(jobPath, buildNum)
	if err != nil {
		return err
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		return f.Output(artifacts, nil)
	}

	if len(artifacts) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "No artifacts for build #%d\n", buildNum)
		return nil
	}

	rows := make([]any, len(artifacts))
	for i := range artifacts {
		rows[i] = artifacts[i]
	}

	columns := []output.Column{
		{Header: "FILE", Field: func(v any) string {
			return v.(jenkins.Artifact).FileName
		}},
		{Header: "PATH", Field: func(v any) string {
			return v.(jenkins.Artifact).RelativePath
		}},
	}

	return f.Output(rows, columns)
}

func downloadArtifact(client *api.Client, jobPath string, buildNum int, name, outputPath string) error {
	// Find the artifact
	artifacts, err := client.GetArtifacts(jobPath, buildNum)
	if err != nil {
		return err
	}

	var match *jenkins.Artifact
	for i := range artifacts {
		if artifacts[i].FileName == name || artifacts[i].RelativePath == name {
			match = &artifacts[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("artifact %q not found in build #%d", name, buildNum)
	}

	body, err := client.DownloadArtifact(jobPath, buildNum, match.RelativePath)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	if outputPath == "" {
		outputPath = filepath.Base(match.FileName)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer func() { _ = f.Close() }()

	n, err := io.Copy(f, body)
	if err != nil {
		return fmt.Errorf("writing artifact: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stderr, "Downloaded %s (%d bytes)\n", outputPath, n)
	return nil
}
