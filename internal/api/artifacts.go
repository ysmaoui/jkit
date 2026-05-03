package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/ysmaoui/jk/internal/jenkins"
)

func (c *Client) GetArtifacts(jobPath string, number int) ([]jenkins.Artifact, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	query := url.Values{"tree": {"artifacts[fileName,relativePath]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting artifacts: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Artifacts []jenkins.Artifact `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding artifacts: %w", err)
	}
	return result.Artifacts, nil
}

func (c *Client) DownloadArtifact(jobPath string, number int, relativePath string) (io.ReadCloser, error) {
	path := fmt.Sprintf("%s/%d/artifact/%s", NormalizeJobPath(jobPath), number, relativePath)

	resp, err := c.Get(path, nil)
	if err != nil {
		return nil, fmt.Errorf("downloading artifact: %w", err)
	}
	return resp.Body, nil
}
