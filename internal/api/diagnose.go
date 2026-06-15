package api

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

// DiagnoseResult is the structured output of build failure analysis.
type DiagnoseResult struct {
	Build        int               `json:"build"`
	Result       string            `json:"result"`
	Duration     string            `json:"duration"`
	URL          string            `json:"url"`
	Cause        string            `json:"cause,omitempty"`
	FailedStages []FailedStage     `json:"failedStages,omitempty"`
	Commits      []CommitSummary   `json:"commits,omitempty"`
	Parameters   map[string]string `json:"parameters,omitempty"`
}

// FailedStage contains a stage name and its extracted error lines.
type FailedStage struct {
	Name   string   `json:"name"`
	Errors []string `json:"errors"`
}

// CommitSummary is a compact commit representation.
type CommitSummary struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Message string `json:"message"`
}

// Diagnose analyzes a build and returns a structured failure summary.
func (c *Client) Diagnose(jobPath string, number int) (*DiagnoseResult, error) {
	build, err := c.GetBuild(jobPath, number)
	if err != nil {
		return nil, err
	}

	d := time.Duration(build.Duration) * time.Millisecond
	if build.Building {
		// Jenkins reports duration=0 while a build is in progress; compute the
		// elapsed time from the start timestamp instead so it isn't shown as "< 1s".
		if elapsed := time.Now().UnixMilli() - build.Timestamp; build.Timestamp > 0 && elapsed > 0 {
			d = time.Duration(elapsed) * time.Millisecond
		}
	}
	res := &DiagnoseResult{
		Build:    build.Number,
		Result:   build.Result,
		Duration: formatAPIDuration(d),
		URL:      build.URL,
		Cause:    build.Cause(),
	}

	if build.Building {
		res.Result = "BUILDING"
		res.Duration += " (running)"
	}

	// Parameters
	if params := build.Parameters(); len(params) > 0 {
		res.Parameters = make(map[string]string, len(params))
		for _, p := range params {
			res.Parameters[p.Name] = p.Value()
		}
	}

	// Commits
	for _, cs := range build.ChangeSets {
		for _, ch := range cs.Items {
			hash := ch.CommitID
			if len(hash) > 7 {
				hash = hash[:7]
			}
			msg := ch.Message
			if idx := strings.IndexAny(msg, "\n\r"); idx >= 0 {
				msg = msg[:idx]
			}
			res.Commits = append(res.Commits, CommitSummary{
				Hash:    hash,
				Author:  ch.Author.FullName,
				Message: msg,
			})
		}
	}

	// If not a failure, no need to dig into stages
	if build.Result == "SUCCESS" || build.Building {
		return res, nil
	}

	// Pipeline stages
	stages, err := c.GetPipelineStages(jobPath, number)
	if err != nil || len(stages) == 0 {
		// Non-pipeline job or Blue Ocean unavailable — try full console log
		res.FailedStages = diagnoseFallbackConsole(c, jobPath, number)
		return res, nil
	}

	// Skip fan-out containers (parallel blocks) — they have no useful logs.
	leaves := jenkins.NonContainerStages(stages)
	var failed []jenkins.Stage
	for _, s := range leaves {
		if s.Status == "FAILURE" || s.Status == "ERROR" {
			failed = append(failed, s)
		}
	}

	// Fetch logs concurrently with bounded parallelism.
	results := make([]FailedStage, len(failed))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, s := range failed {
		wg.Add(1)
		go func(i int, s jenkins.Stage) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = FailedStage{Name: s.Name}
			log, logErr := c.GetStageLog(jobPath, number, s.ID)
			if logErr == nil {
				log = output.SanitizeLog(log)
				results[i].Errors = extractErrors(log)
			}
		}(i, s)
	}
	wg.Wait()
	res.FailedStages = results

	return res, nil
}

// diagnoseFallbackConsole extracts errors from the console log when stages
// aren't available. It scans the tail, not the head — build failures surface at
// the end, and on a large log the head holds only setup noise.
func diagnoseFallbackConsole(c *Client, jobPath string, number int) []FailedStage {
	const tailWindow = 4 << 20 // 4 MB
	text, err := c.GetBuildLogTail(jobPath, number, tailWindow)
	if err != nil || text == "" {
		return nil
	}
	log := output.SanitizeLog(text)
	errors := extractErrors(log)
	if len(errors) == 0 {
		return nil
	}
	return []FailedStage{{Name: "(console)", Errors: errors}}
}

// extractErrors pulls error-relevant lines from a log string.
func extractErrors(log string) []string {
	lines := strings.Split(log, "\n")
	var errors []string
	seen := make(map[string]bool)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if isErrorLine(lower) && !seen[trimmed] {
			seen[trimmed] = true
			errors = append(errors, trimmed)
		}
	}

	// Cap at 30 lines to avoid overwhelming output
	if len(errors) > 30 {
		errors = errors[len(errors)-30:]
	}
	return errors
}

var errorPatterns = []string{
	"error", "fail", "exception", "fatal",
	"unable to", "cannot ", "could not",
	"no such", "not found", "timed out",
	"rejected", "denied", "aborted",
}

func isErrorLine(lower string) bool {
	for _, p := range errorPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Stack trace lines (indented with "at " prefix)
	if strings.HasPrefix(lower, "at ") || strings.HasPrefix(lower, "\tat ") {
		return true
	}
	return false
}

func formatAPIDuration(d time.Duration) string {
	if d < time.Second {
		return "< 1s"
	}
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if m < 60 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh%dm", h, m)
}
