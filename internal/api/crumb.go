package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type crumbInfo struct {
	Crumb             string `json:"crumb"`
	CrumbRequestField string `json:"crumbRequestField"`
}

type crumbIssuer struct {
	mu     sync.Mutex
	client *Client
	crumb  *crumbInfo
}

func newCrumbIssuer(c *Client) *crumbIssuer {
	return &crumbIssuer{client: c}
}

func (ci *crumbIssuer) ensureCrumb() (*crumbInfo, error) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if ci.crumb != nil {
		return ci.crumb, nil
	}
	return ci.fetchCrumb()
}

func (ci *crumbIssuer) fetchCrumb() (*crumbInfo, error) {
	u := ci.client.host + "/crumbIssuer/api/json"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ci.client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// CSRF protection disabled on this Jenkins
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching crumb: HTTP %d", resp.StatusCode)
	}
	var info crumbInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding crumb: %w", err)
	}
	ci.crumb = &info
	return &info, nil
}

func (ci *crumbIssuer) invalidate() {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.crumb = nil
}
