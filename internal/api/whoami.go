package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
// backoff of Get so that a dead host costs one client timeout. It follows a
// redirect only to where the credentials go, such as http to https on the same
// host behind a TLS-terminating proxy. Any other redirect, e.g. to an SSO
// login page, is returned as the 3xx answer.
func (c *Client) WhoAmIStatus(ctx context.Context) (int, *WhoAmI, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/whoAmI/api/json", nil)
	if err != nil {
		return 0, nil, err
	}
	home, err := url.Parse(c.host)
	if err != nil {
		return 0, nil, err
	}
	creds := &authTransport{home: home}
	hc := *c.httpClient
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !creds.sendsCredentials(next.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
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
