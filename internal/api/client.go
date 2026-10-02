package api

import (
	"bytes"
	"context"
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ysmaoui/jkit/internal/jenkins"
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
	stageLogCap    int
	// consoleTailWindow is the largest window ConsoleTailLines reads.
	consoleTailWindow int

	versionMu    sync.Mutex
	version      string
	versionKnown bool
	// plainProgressive is set once the server has answered progressiveText
	// without multipart.
	plainProgressive atomic.Bool
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

// WithStageLogCap overrides how many bytes of a stage log one read keeps.
func WithStageLogCap(n int) ClientOption {
	return func(c *Client) {
		c.stageLogCap = n
	}
}

// StageLogCap returns how many bytes of a stage log one read keeps.
func (c *Client) StageLogCap() int { return c.stageLogCap }

// WithConsoleTailWindow overrides the largest tail window, in bytes, that
// ConsoleTailLines reads.
func WithConsoleTailWindow(n int) ClientOption {
	return func(c *Client) {
		c.consoleTailWindow = n
	}
}

// ConsoleTailWindow returns the largest tail window, in bytes, that
// ConsoleTailLines reads.
func (c *Client) ConsoleTailWindow() int { return c.consoleTailWindow }

// PipelineSource returns the configured backend selector.
func (c *Client) PipelineSource() PipelineSource { return c.pipelineSource }

type verboseTransport struct {
	base http.RoundTripper
}

func (t *verboseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	_, _ = fmt.Fprintf(os.Stderr, "> %s %s\n", req.Method, req.URL.Path)
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "< error: %s (%s)\n", err, elapsed.Round(time.Millisecond))
		return nil, err
	}
	_, _ = fmt.Fprintf(os.Stderr, "< %d %s (%s)\n", resp.StatusCode, http.StatusText(resp.StatusCode), elapsed.Round(time.Millisecond))
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
	c.stageLogCap = defaultStageLogCap
	c.consoleTailWindow = defaultTailWindow
	c.pipelineSource = parsePipelineSource(os.Getenv("JKIT_PIPELINE_SOURCE"))
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

// getContext is Get bound to ctx, with extra request headers. Cancelling ctx
// aborts the request, a retry wait, or reading the body.
func (c *Client) getContext(ctx context.Context, path string, query url.Values, header http.Header) (*http.Response, error) {
	u := c.host + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return c.do(ctx, "GET", u, nil, header)
}

// CloseBody is a convenience helper to discard and close a response body.
func CloseBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
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
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if crumb != nil {
		header.Set(crumb.CrumbRequestField, crumb.Crumb)
	}
	return c.do(context.Background(), method, rawURL, bodyFn, header)
}

func (c *Client) do(ctx context.Context, method, rawURL string, bodyFn func() io.Reader, header http.Header) (*http.Response, error) {
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
		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		for k, v := range header {
			req.Header[k] = v
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < maxRetries {
				if err := sleepCtx(ctx, backoff(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &jenkins.UnreachableError{Host: c.host, Cause: err}
		}

		if v := resp.Header.Get("X-Jenkins"); v != "" {
			c.setVersion(v)
		}

		if resp.StatusCode == http.StatusServiceUnavailable && attempt < maxRetries {
			CloseBody(resp)
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}

		if err := checkResponse(resp); err != nil {
			return nil, err
		}
		return resp, nil
	}
	return nil, fmt.Errorf("max retries exceeded for %s", rawURL)
}

func (c *Client) setVersion(v string) {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	c.version, c.versionKnown = v, true
}

// serverVersion returns the Jenkins version from X-Jenkins, "" when the server
// does not say. Jenkins sets the header on api/json and pages but not on every
// route, progressiveText included, so when no answer has carried it yet the
// root api/json is asked once.
func (c *Client) serverVersion(ctx context.Context) string {
	c.versionMu.Lock()
	v, known := c.version, c.versionKnown
	c.versionMu.Unlock()
	if known {
		return v
	}
	resp, err := c.getContext(ctx, "/api/json", url.Values{"tree": {"_class"}}, nil)
	if err == nil {
		CloseBody(resp)
	}
	// Only an answer settles the version: a network failure or a server
	// error is asked again next time.
	var unreachable *jenkins.UnreachableError
	var se *jenkins.ServerError
	if ctx.Err() != nil || errors.As(err, &unreachable) || errors.As(err, &se) && se.StatusCode >= 500 {
		return ""
	}
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	c.versionKnown = true
	return c.version
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	return time.Duration(math.Pow(2, float64(attempt))) * 500 * time.Millisecond
}

func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
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
