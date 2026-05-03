package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ysmaoui/jk/internal/jenkins"
)

// PipelineSource selects which backend to use for pipeline stage/log queries.
type PipelineSource int

const (
	PipelineSourceAuto      PipelineSource = iota // try PGV, fall back to Blue Ocean on 404
	PipelineSourcePGV                             // PGV only, error if unavailable
	PipelineSourceBlueOcean                       // Blue Ocean only (opt-out for slow PGV)
)

func parsePipelineSource(s string) PipelineSource {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pgv", "pipeline-graph-view":
		return PipelineSourcePGV
	case "blueocean", "blue-ocean", "blue":
		return PipelineSourceBlueOcean
	default:
		return PipelineSourceAuto
	}
}

type Client struct {
	httpClient     *http.Client
	host           string
	user           string
	token          string
	crumbs         *crumbIssuer
	verbose        bool
	pipelineSource PipelineSource
}

type authTransport struct {
	base  http.RoundTripper
	user  string
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	cred := base64.StdEncoding.EncodeToString([]byte(t.user + ":" + t.token))
	req.Header.Set("Authorization", "Basic "+cred)
	return t.base.RoundTrip(req)
}

// ClientOption configures the API client.
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithVerbose enables HTTP request/response logging to stderr.
func WithVerbose() ClientOption {
	return func(c *Client) {
		c.verbose = true
	}
}

// WithPipelineSource overrides the stage/log backend selection.
func WithPipelineSource(src PipelineSource) ClientOption {
	return func(c *Client) {
		c.pipelineSource = src
	}
}

// PipelineSource returns the configured backend selector.
func (c *Client) PipelineSource() PipelineSource { return c.pipelineSource }

type verboseTransport struct {
	base http.RoundTripper
}

func (t *verboseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Fprintf(os.Stderr, "> %s %s\n", req.Method, req.URL.Path)
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "< error: %s (%s)\n", err, elapsed.Round(time.Millisecond))
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "< %d %s (%s)\n", resp.StatusCode, http.StatusText(resp.StatusCode), elapsed.Round(time.Millisecond))
	return resp, nil
}

func NewClient(host, user, token string, opts ...ClientOption) *Client {
	host = strings.TrimRight(host, "/")
	jar, _ := cookiejar.New(nil)
	c := &Client{
		httpClient: &http.Client{
			Transport: &authTransport{
				base:  http.DefaultTransport,
				user:  user,
				token: token,
			},
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
		host:  host,
		user:  user,
		token: token,
	}
	c.crumbs = newCrumbIssuer(c)
	c.pipelineSource = parsePipelineSource(os.Getenv("JK_PIPELINE_SOURCE"))
	for _, opt := range opts {
		opt(c)
	}
	if c.verbose {
		c.httpClient.Transport = &verboseTransport{base: c.httpClient.Transport}
	}
	return c
}

func (c *Client) Host() string { return c.host }

func (c *Client) Get(path string, query url.Values) (*http.Response, error) {
	u := c.host + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return c.doWithRetry("GET", u, nil, "", nil)
}

// CloseBody is a convenience helper to discard and close a response body.
func CloseBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func (c *Client) Post(path string, body io.Reader, contentType string) (*http.Response, error) {
	u := c.host + path

	// Buffer body so it can be replayed on crumb-retry
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
	}
	var bodyReader func() io.Reader
	if bodyBytes != nil {
		bodyReader = func() io.Reader {
			return bytes.NewReader(bodyBytes)
		}
	}

	crumb, err := c.crumbs.ensureCrumb()
	if err != nil {
		return nil, fmt.Errorf("obtaining crumb: %w", err)
	}

	resp, err := c.doWithRetry("POST", u, bodyReader, contentType, crumb)
	if err != nil {
		var permErr *jenkins.PermissionError
		if errors.As(err, &permErr) {
			// 403 likely means stale crumb — invalidate, re-fetch, retry once
			c.crumbs.invalidate()
			crumb, err = c.crumbs.ensureCrumb()
			if err != nil {
				return nil, fmt.Errorf("re-fetching crumb: %w", err)
			}
			return c.doWithRetry("POST", u, bodyReader, contentType, crumb)
		}
		return nil, err
	}
	return resp, nil
}

func (c *Client) doWithRetry(method, rawURL string, bodyFn func() io.Reader, contentType string, crumb *crumbInfo) (*http.Response, error) {
	// Only retry idempotent methods (GET, HEAD, OPTIONS)
	maxRetries := 3
	if method != "GET" && method != "HEAD" && method != "OPTIONS" {
		maxRetries = 0
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var body io.Reader
		if bodyFn != nil {
			body = bodyFn()
		}
		req, err := http.NewRequest(method, rawURL, body)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if crumb != nil {
			req.Header.Set(crumb.CrumbRequestField, crumb.Crumb)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, &jenkins.UnreachableError{Host: c.host, Cause: err}
		}

		if resp.StatusCode == http.StatusServiceUnavailable && attempt < maxRetries {
			CloseBody(resp)
			time.Sleep(backoff(attempt))
			continue
		}

		if err := checkResponse(resp); err != nil {
			return nil, err
		}
		return resp, nil
	}
	return nil, fmt.Errorf("max retries exceeded for %s", rawURL)
}

func backoff(attempt int) time.Duration {
	return time.Duration(math.Pow(2, float64(attempt))) * 500 * time.Millisecond
}

func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))
	host := resp.Request.URL.Scheme + "://" + resp.Request.URL.Host

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &jenkins.AuthError{Host: host}
	case http.StatusForbidden:
		return &jenkins.PermissionError{
			Resource: resp.Request.URL.Path,
			Host:     host,
		}
	case http.StatusNotFound:
		return &jenkins.NotFoundError{
			Resource: "resource",
			Name:     resp.Request.URL.Path,
			Host:     host,
		}
	default:
		return &jenkins.ServerError{
			Host:       host,
			Path:       resp.Request.URL.Path,
			StatusCode: resp.StatusCode,
			Body:       msg,
		}
	}
}

// NormalizeJobPath converts "team/svc" to "/job/team/job/svc".
// Each segment is URL-path-escaped for safety with special characters.
func NormalizeJobPath(natural string) string {
	natural = strings.Trim(natural, "/")
	if natural == "" {
		return ""
	}
	parts := strings.Split(natural, "/")
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("/job/")
		b.WriteString(url.PathEscape(p))
	}
	return b.String()
}
