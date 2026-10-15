package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Raw sends an authenticated request and returns the response whatever its
// status, so the caller can show an error body. target is a full http(s) URL
// under the client's host, or a path, with optional query, relative to it;
// either is sent as given, the query byte for byte.
//
// A method other than GET, HEAD or OPTIONS does not follow redirects, so the
// caller sees what Jenkins answered rather than the page it points to. It
// carries a crumb when one can be had, and one rejected with 403 is re-fetched
// once. Without a crumb the request is still sent and the server's answer
// returned.
func (c *Client) Raw(ctx context.Context, method, target string, header http.Header) (*http.Response, error) {
	u := target
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		if target != c.host && !strings.HasPrefix(target, c.host+"/") && !strings.HasPrefix(target, c.host+"?") {
			return nil, fmt.Errorf("%s is not on %s", target, c.host)
		}
	} else {
		u = c.host + "/" + strings.TrimLeft(target, "/")
	}
	if isIdempotent(method) {
		return c.send(ctx, method, u, nil, header)
	}

	hc := *c.httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	withCrumb := func() (*http.Response, bool, error) {
		h := header.Clone()
		if h == nil {
			h = http.Header{}
		}
		// A failed crumb fetch is not reported here: the request itself shows
		// whether the server needed one, with its own status and body.
		crumb, _ := c.crumbs.ensureCrumb()
		if crumb != nil {
			h.Set(crumb.CrumbRequestField, crumb.Crumb)
		}
		resp, err := c.sendWith(ctx, &hc, method, u, nil, h)
		return resp, crumb != nil, err
	}
	resp, sentCrumb, err := withCrumb()
	if err != nil || !sentCrumb || resp.StatusCode != http.StatusForbidden {
		return resp, err
	}
	CloseBody(resp)
	c.crumbs.invalidate()
	resp, _, err = withCrumb()
	return resp, err
}
