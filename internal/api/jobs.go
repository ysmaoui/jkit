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
func (c *Client) ListJobsRecursive(folder string) ([]jenkins.Job, error) {
	jobs, err := c.ListJobTree(folder)
	if err != nil {
		return nil, err
	}
	return flattenJobs(jobs), nil
}

// jobTreeDepth is how many levels of nesting one tree query asks for. Jenkins
// truncates below that silently, so ListJobTree re-queries containers sitting
// at the limit rather than trusting an empty child list.
const jobTreeDepth = 5

// ListJobTree lists a folder's contents with the nesting kept. Callers that
// mirror the layout onto something else need the containers and each job's
// position, both of which flattenJobs drops.
//
// One query covers jobTreeDepth levels; anything deeper costs an extra request
// per container at the boundary. Deep hierarchies are rare, so the common case
// stays a single call.
func (c *Client) ListJobTree(folder string) ([]jenkins.Job, error) {
	jobs, err := c.listJobTreePage(folder)
	if err != nil {
		return nil, err
	}
	if err := c.fillTruncatedFolders(jobs, 1); err != nil {
		return nil, err
	}
	return jobs, nil
}

// fillTruncatedFolders re-queries every container found at the depth limit. A
// container whose children were cut off comes back indistinguishable from one
// that is genuinely empty, so the only honest option is to ask again. An export
// that quietly omits jobs is worse than a slower one.
func (c *Client) fillTruncatedFolders(jobs []jenkins.Job, depth int) error {
	for i := range jobs {
		if !jobs[i].IsContainer() {
			continue
		}
		if depth < jobTreeDepth {
			if err := c.fillTruncatedFolders(jobs[i].Jobs, depth+1); err != nil {
				return err
			}
			continue
		}
		children, err := c.listJobTreePage(jobs[i].FullName)
		if err != nil {
			return fmt.Errorf("listing %s below the tree query depth: %w", jobs[i].FullName, err)
		}
		jobs[i].Jobs = children
		if err := c.fillTruncatedFolders(jobs[i].Jobs, 1); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) listJobTreePage(folder string) ([]jenkins.Job, error) {
	path := "/api/json"
	if folder != "" {
		path = NormalizeJobPath(folder) + "/api/json"
	}
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
	return result.Jobs, nil
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
