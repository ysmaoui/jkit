package api

import (
	"errors"
	"fmt"
	"io"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// GetJobConfigXML returns a job's raw config.xml. It is the only source for SCM
// source, trait and script-path detail: /api/json reports a multibranch job's
// sources as empty objects because none of their fields are exported.
//
// Reading config.xml needs Job/ExtendedRead (or Job/Configure); a 403 surfaces
// as a PermissionError naming that.
func (c *Client) GetJobConfigXML(jobPath string) ([]byte, error) {
	path := NormalizeJobPath(jobPath) + "/config.xml"

	resp, err := c.Get(path, nil)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, &jenkins.NotFoundError{Resource: "job", Name: jobPath, Host: c.host}
		}
		var pe *jenkins.PermissionError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("cannot read config.xml for %s: reading a job's configuration needs the Job/ExtendedRead permission on %s: %w", jobPath, c.host, err)
		}
		return nil, fmt.Errorf("getting job config.xml: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading job config.xml: %w", err)
	}
	return data, nil
}

// GetJobDefinition fetches and parses a job's config.xml into the inspect view.
func (c *Client) GetJobDefinition(jobPath string) (*jenkins.JobDefinition, error) {
	data, err := c.GetJobConfigXML(jobPath)
	if err != nil {
		return nil, err
	}
	def, err := jenkins.ParseJobConfig(data)
	if err != nil {
		return nil, err
	}
	def.SetJobPath(jobPath)
	return def, nil
}
