package api

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func (c *Client) ListJobs(folder string) ([]jenkins.Job, error) {
	path := "/api/json"
	if folder != "" {
		path = NormalizeJobPath(folder) + "/api/json"
	}
	query := url.Values{"tree": {"jobs[name,url,color,_class,lastBuild[number,result]]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Jobs []jenkins.Job `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding jobs: %w", err)
	}
	return result.Jobs, nil
}

// ListJobsRecursive lists jobs recursively, flattening folders into a single list.
// Uses nested tree queries (5 levels deep) for a single API call.
func (c *Client) ListJobsRecursive(folder string) ([]jenkins.Job, error) {
	path := "/api/json"
	if folder != "" {
		path = NormalizeJobPath(folder) + "/api/json"
	}
	// 5-level nested tree query
	leaf := "name,fullName,url,color,_class,lastBuild[number,result]"
	tree := leaf + ",jobs[" + leaf + ",jobs[" + leaf + ",jobs[" + leaf + ",jobs[" + leaf + "]]]]"
	query := url.Values{"tree": {"jobs[" + tree + "]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("listing jobs recursively: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Jobs []jenkins.Job `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding jobs: %w", err)
	}
	return flattenJobs(result.Jobs), nil
}

// flattenJobs recursively collects all non-folder jobs, preserving folders as markers.
func flattenJobs(jobs []jenkins.Job) []jenkins.Job {
	var flat []jenkins.Job
	for _, j := range jobs {
		if j.IsFolder() {
			flat = append(flat, flattenJobs(j.Jobs)...)
		} else {
			flat = append(flat, j)
		}
	}
	return flat
}

// inspectContainer fetches a job's class and immediate child jobs. It is used to
// turn a bare 404 on a build request into a helpful error when the job is really
// a folder or multibranch pipeline (which have no builds of their own).
func (c *Client) inspectContainer(jobPath string) (*jenkins.Job, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	query := url.Values{"tree": {"_class,name,fullName,jobs[name,_class]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var job jenkins.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

// GetJobParameters returns the parameter definitions a job accepts. A job with no
// ParametersDefinitionProperty returns an empty slice (not an error).
func (c *Client) GetJobParameters(jobPath string) ([]jenkins.ParameterDefinition, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	query := url.Values{"tree": {"property[parameterDefinitions[_class,name,type,description,defaultParameterValue[value],choices]]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting job parameters: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Property []struct {
			ParameterDefinitions []jenkins.ParameterDefinition `json:"parameterDefinitions"`
		} `json:"property"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding job parameters: %w", err)
	}

	for _, p := range result.Property {
		if len(p.ParameterDefinitions) > 0 {
			return p.ParameterDefinitions, nil
		}
	}
	return nil, nil
}

func (c *Client) GetJob(jobPath string) (*jenkins.Job, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	query := url.Values{"tree": {"name,fullName,url,color,lastBuild[number,result,timestamp,duration,building],inQueue"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting job: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var job jenkins.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("decoding job: %w", err)
	}
	return &job, nil
}
