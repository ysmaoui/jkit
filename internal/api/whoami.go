package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// WhoAmI is the identity Jenkins attaches to a request.
type WhoAmI struct {
	Name      string `json:"name"`
	Anonymous bool   `json:"anonymous"`
}

// whoAmIMaxBody bounds the read of a 2xx answer that may be a large HTML page
// rather than whoAmI JSON.
const whoAmIMaxBody = 64 << 10

// WhoAmIStatus requests /whoAmI/api/json once and returns the HTTP status code
// and, for a 2xx answer that decodes, the identity. It skips the retry and
// backoff of Get so that a dead host costs one client timeout, and does not
// follow redirects: a redirect to a login page would otherwise end in a 200.
func (c *Client) WhoAmIStatus(ctx context.Context) (int, *WhoAmI, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/whoAmI/api/json", nil)
	if err != nil {
		return 0, nil, err
	}
	hc := *c.httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer CloseBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil, nil
	}
	var who WhoAmI
	if err := json.NewDecoder(io.LimitReader(resp.Body, whoAmIMaxBody)).Decode(&who); err != nil {
		return resp.StatusCode, nil, nil
	}
	return resp.StatusCode, &who, nil
}
