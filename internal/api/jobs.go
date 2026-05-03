package api

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jk/internal/jenkins"
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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

func (c *Client) GetJob(jobPath string) (*jenkins.Job, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	query := url.Values{"tree": {"name,fullName,url,color,lastBuild[number,result,timestamp,duration,building],inQueue"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting job: %w", err)
	}
	defer resp.Body.Close()

	var job jenkins.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("decoding job: %w", err)
	}
	return &job, nil
}
