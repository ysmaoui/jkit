package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func (c *Client) GetTestReport(jobPath string, number int) (*jenkins.TestReport, error) {
	path := fmt.Sprintf("%s/%d/testReport/api/json", NormalizeJobPath(jobPath), number)
	tree := "duration,failCount,passCount,skipCount,suites[name,cases[className,name,status,duration,errorDetails]]"
	query := url.Values{"tree": {tree}}

	resp, err := c.Get(path, query)
	if err != nil {
		var nfe *jenkins.NotFoundError
		if errors.As(err, &nfe) {
			return nil, nil // no test report
		}
		return nil, fmt.Errorf("getting test report: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var report jenkins.TestReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("decoding test report: %w", err)
	}
	return &report, nil
}
