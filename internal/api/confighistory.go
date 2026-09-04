package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if result.Entries == nil {
		// An empty array and a body without the key both mean no history; a
		// caller piping --json through jq should not have to handle two shapes.
		return []jenkins.ConfigChange{}, nil
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

// GetJobConfigRevision returns the config.xml the JobConfigHistory plugin
// stored at timestamp, which is the "date" field of a history entry.
//
// The endpoint answers 200 for a timestamp it does not hold, with a zero-byte
// body and no other marker, so an empty response is treated as the error and
// named after the timestamp that produced it. type=raw and type=xml return the
// same bytes; the parameter only picks the Content-Type.
//
// With Job/ExtendedRead but not Job/Configure the controller masks the secrets
// in what it returns, so two revisions can differ in hidden values alone.
func (c *Client) GetJobConfigRevision(jobPath, timestamp string) ([]byte, error) {
	path := NormalizeJobPath(jobPath) + "/jobConfigHistory/configOutput"
	query := url.Values{"type": {"raw"}, "timestamp": {timestamp}}

	resp, err := c.Get(path, query)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, c.explainConfigHistory404(jobPath)
		}
		var pe *jenkins.PermissionError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("cannot read revision %s of %s: reading a stored configuration needs the Job/ExtendedRead permission on %s: %w", timestamp, jobPath, c.host, err)
		}
		return nil, fmt.Errorf("getting revision %s of %s: %w", timestamp, jobPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading revision %s of %s: %w", timestamp, jobPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%s has no stored configuration at %s: the plugin answers with an empty body for a timestamp it does not hold, and for an entry whose config it no longer keeps — jkit inspect %s --history lists the timestamps it does have", jobPath, timestamp, jobPath)
	}
	return data, nil
}
