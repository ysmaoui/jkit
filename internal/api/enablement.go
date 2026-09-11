package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// JobEnablement is a job's current run state and whether its type can be
// toggled at all.
type JobEnablement struct {
	JobPath  string `json:"jobPath"`
	Class    string `json:"class"`
	Disabled bool   `json:"disabled"`
	// Supported is false for a job type that does not implement
	// supportsMakeDisabled, which Jenkins defaults to false. Such a job reports
	// no disabled field at all.
	Supported bool   `json:"supported"`
	Parent    string `json:"parent,omitempty"`
}

// GetJobEnablement reads whether a job is disabled. A job type that cannot be
// disabled omits the field entirely rather than reporting false, so absence is
// the signal that the type does not support it.
func (c *Client) GetJobEnablement(jobPath string) (*JobEnablement, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	resp, err := c.Get(path, url.Values{"tree": {"_class,disabled"}})
	if err != nil {
		return nil, err
	}
	defer CloseBody(resp)

	var raw struct {
		Class    string `json:"_class"`
		Disabled *bool  `json:"disabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding job state: %w", err)
	}
	st := &JobEnablement{JobPath: jobPath, Class: raw.Class, Supported: raw.Disabled != nil}
	if raw.Disabled != nil {
		st.Disabled = *raw.Disabled
	}
	return st, nil
}

// SetJobEnabled posts to enable or disable, then re-reads the state rather than
// assuming the POST did what was asked: Jenkins answers with a redirect, not
// with the resulting state.
func (c *Client) SetJobEnabled(jobPath string, enabled bool) (*JobEnablement, error) {
	verb := "disable"
	if enabled {
		verb = "enable"
	}

	resp, err := c.Post(NormalizeJobPath(jobPath)+"/"+verb, nil, "")
	if err != nil {
		return nil, c.explainEnablementFailure(jobPath, verb, err)
	}
	CloseBody(resp)

	return c.GetJobEnablement(jobPath)
}

// explainEnablementFailure separates the three reasons a toggle is refused, so
// none of them is reported as another. doDisable checks Item.CONFIGURE, and the
// route exists only for a job type whose supportsMakeDisabled is true, which
// Jenkins defaults to false.
func (c *Client) explainEnablementFailure(jobPath, verb string, err error) error {
	var pe *jenkins.PermissionError
	if errors.As(err, &pe) {
		return fmt.Errorf("cannot %s %s — that needs the Job/Configure permission on it", verb, jobPath)
	}

	var nfe *jenkins.NotFoundError
	if !errors.As(err, &nfe) {
		return err
	}
	st, probeErr := c.GetJobEnablement(jobPath)
	switch {
	case probeErr != nil:
		return fmt.Errorf("no job %s on %s", jobPath, c.host)
	case !st.Supported:
		return fmt.Errorf("%s cannot be %sd: a %s has no enabled state to toggle", jobPath, verb, jenkins.KindFromClass(st.Class))
	}
	return fmt.Errorf("%s does not answer /%s even though it reports an enabled state — its type may not implement the toggle", jobPath, verb)
}
