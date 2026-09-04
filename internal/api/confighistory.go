package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// GetJobConfigHistory returns the job's configuration change log, newest first,
// from the JobConfigHistory plugin.
//
// The list is bounded by the plugin's per-job retention (maxHistoryEntries) and
// the instance-wide maxEntriesPerPage, which truncates the response with no
// marker: it is what the server still holds, never every change ever made.
// A caller without Job/Configure or Job/ExtendedRead gets 200 and an empty list
// rather than a 403, so an empty result is not proof that nothing changed.
func (c *Client) GetJobConfigHistory(jobPath string) ([]jenkins.ConfigChange, error) {
	path := NormalizeJobPath(jobPath) + "/jobConfigHistory/api/json"

	resp, err := c.Get(path, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, c.explainConfigHistory404(jobPath)
		}
		return nil, fmt.Errorf("getting job config history: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Entries []jenkins.ConfigChange `json:"jobConfigHistory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding job config history: %w", err)
	}
	return result.Entries, nil
}

// explainConfigHistory404 decides which of two different failures produced the
// 404. A Jenkins without the JobConfigHistory plugin serves the same generic
// 404 page for /jobConfigHistory as it does for a job that does not exist, so
// the job itself is re-requested: if it answers, the plugin is what is missing.
func (c *Client) explainConfigHistory404(jobPath string) error {
	resp, err := c.Get(NormalizeJobPath(jobPath)+"/api/json", url.Values{"tree": {"name"}})
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return &jenkins.NotFoundError{Resource: "job", Name: jobPath, Host: c.host}
		}
		return fmt.Errorf("checking whether %s exists after its config history 404'd: %w", jobPath, err)
	}
	CloseBody(resp)
	return fmt.Errorf("no config history for %s — the job exists, so the JobConfigHistory plugin (which exposes /jobConfigHistory) is not installed on %s", jobPath, c.host)
}
