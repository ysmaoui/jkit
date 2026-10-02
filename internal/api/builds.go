package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// ContainerHint returns a ContainerBuildError if jobPath is actually a folder or
// multibranch pipeline (neither has builds of its own), listing its child jobs so
// the caller can retry with the right target. It returns nil if jobPath is a
// normal job or cannot be inspected. Callers use it to turn a bare 404 or an
// empty result into actionable guidance.
func (c *Client) ContainerHint(jobPath string) *jenkins.ContainerBuildError {
	job, err := c.inspectContainer(jobPath)
	if err != nil || job == nil || !job.IsContainer() {
		return nil
	}
	kind := "folder"
	if job.IsMultibranch() {
		kind = "multibranch pipeline"
	}
	children := make([]string, 0, len(job.Jobs))
	for _, child := range job.Jobs {
		children = append(children, child.Name)
	}
	return &jenkins.ContainerBuildError{
		JobPath:  jobPath,
		Kind:     kind,
		Children: children,
		Host:     c.host,
	}
}

// enrichNotFound upgrades a NotFoundError on a build request into a
// ContainerBuildError when jobPath is really a container. Any other error (or a
// normal job) is returned unchanged.
func (c *Client) enrichNotFound(jobPath string, err error) error {
	var nfe *jenkins.NotFoundError
	if !errors.As(err, &nfe) {
		return err
	}
	if hint := c.ContainerHint(jobPath); hint != nil {
		return hint
	}
	return err
}

func (c *Client) GetBuilds(jobPath string, limit int) ([]jenkins.Build, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	tree := fmt.Sprintf("builds[number,result,timestamp,duration,building]{0,%d}", limit)
	query := url.Values{"tree": {tree}}

	resp, err := c.Get(path, query)
	if err != nil {
		return nil, fmt.Errorf("getting builds: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Builds []jenkins.Build `json:"builds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding builds: %w", err)
	}
	return result.Builds, nil
}

// GetBuildHistory returns the last `limit` builds with trigger causes included,
// for trend/history views. Unlike GetBuilds it fetches the CauseAction so each
// build's Cause() is populated.
func (c *Client) GetBuildHistory(jobPath string, limit int) ([]jenkins.Build, error) {
	path := NormalizeJobPath(jobPath) + "/api/json"
	tree := fmt.Sprintf("builds[number,result,timestamp,duration,building,actions[_class,causes[shortDescription,_class]]]{0,%d}", limit)
	query := url.Values{"tree": {tree}}

	resp, err := c.Get(path, query)
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting build history: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Builds []jenkins.Build `json:"builds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding build history: %w", err)
	}
	return result.Builds, nil
}

func (c *Client) GetBuild(jobPath string, number int) (*jenkins.Build, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	query := url.Values{"tree": {"number,result,timestamp,duration,building,url,actions[_class,parameters[name,value],causes[shortDescription,_class]],changeSets[items[commitId,msg,author[fullName],timestamp]]"}}

	resp, err := c.Get(path, query)
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var build jenkins.Build
	if err := json.NewDecoder(resp.Body).Decode(&build); err != nil {
		return nil, fmt.Errorf("decoding build: %w", err)
	}
	return &build, nil
}

// IsBuilding reports whether a build is still running, fetching only that flag.
func (c *Client) IsBuilding(jobPath string, number int) (bool, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"tree": {"building"}})
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return false, e
		}
		return false, fmt.Errorf("getting build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var build struct {
		Building bool `json:"building"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&build); err != nil {
		return false, fmt.Errorf("decoding build: %w", err)
	}
	return build.Building, nil
}

// GetBuildEnv returns a build's environment variables and where they came from.
// EnvInject's /injectedEnvVars only exists when a job actually used the plugin,
// which pipeline jobs never do, so a 404 there is the normal case rather than a
// missing plugin; the pipeline run's own EnvActionImpl is tried next.
func (c *Client) GetBuildEnv(jobPath string, number int) (*jenkins.BuildEnv, error) {
	injected, err := c.injectedEnv(jobPath, number)
	if err == nil {
		return &jenkins.BuildEnv{Source: jenkins.EnvSourceInjected, Vars: injected}, nil
	}
	var nfe *jenkins.NotFoundError
	if !errors.As(err, &nfe) {
		return nil, err
	}

	pipeline, perr := c.pipelineEnv(jobPath, number)
	switch {
	case perr == nil && len(pipeline) > 0:
		return &jenkins.BuildEnv{Source: jenkins.EnvSourcePipeline, Vars: pipeline}, nil
	case perr != nil && errors.As(perr, &nfe):
		if hint := c.ContainerHint(jobPath); hint != nil {
			return nil, hint
		}
		return nil, fmt.Errorf("no build %s #%d on %s", jobPath, number, c.host)
	case perr != nil:
		return nil, perr
	}
	return nil, fmt.Errorf("no environment variables recorded for %s #%d — the build exists but injected no variables (EnvInject) and its script assigned none to env.* (pipeline)", jobPath, number)
}

func (c *Client) injectedEnv(jobPath string, number int) (map[string]string, error) {
	path := fmt.Sprintf("%s/%d/injectedEnvVars/api/json", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		EnvMap map[string]string `json:"envMap"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding build env: %w", err)
	}
	return result.EnvMap, nil
}

// pipelineEnv reads EnvActionImpl, which carries only what the script assigned
// to env.*.
func (c *Client) pipelineEnv(jobPath string, number int) (map[string]string, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"tree": {"actions[environment]"}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Actions []struct {
			Environment map[string]string `json:"environment"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding pipeline env: %w", err)
	}
	for _, a := range result.Actions {
		if len(a.Environment) > 0 {
			return a.Environment, nil
		}
	}
	return nil, nil
}

// TriggerResult is the outcome of TriggerBuild. Indexing is true when the target
// was a multibranch pipeline or organization folder: POST /build starts a scan
// there instead of queueing a build, so there is no queue item (QueueID is 0).
// OrgFolder is true when that target was an organization folder, whose branch
// jobs sit one level deeper (<org>/<repo>/<branch>) than a multibranch project's.
type TriggerResult struct {
	QueueID   int
	Indexing  bool
	OrgFolder bool
}

// TriggerBuild queues a build, or starts a scan on a multibranch pipeline or
// organization folder. On a parameterized job Jenkins answers a bare POST /build
// with 400 "Nothing is submitted" (it expects the UI's json form), while
// /buildWithParameters fills every omitted parameter with its default and
// rejects unparameterized jobs, so with no params the route follows the job's
// live definitions. A multibranch branch job has none until its first run
// executes properties(). With params the job is not looked up first: a branch
// source has no /buildWithParameters, and only that failure pays for the lookup.
func (c *Client) TriggerBuild(jobPath string, params map[string]string) (*TriggerResult, error) {
	parameterized := len(params) > 0
	indexing, orgFolder := false, false
	if !parameterized {
		class, defs, err := c.getJobClassAndParameters(jobPath)
		if err != nil {
			return nil, fmt.Errorf("triggering build: %w", err)
		}
		job := jenkins.Job{Class: class}
		indexing, orgFolder = job.IsBranchSource(), job.IsOrgFolder()
		parameterized = len(defs) > 0
	}

	path := NormalizeJobPath(jobPath)
	var body io.Reader
	contentType := ""

	if parameterized {
		path += "/buildWithParameters"
		form := url.Values{}
		for k, v := range params {
			form.Set(k, v)
		}
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else {
		path += "/build"
	}

	resp, err := c.Post(path, body, contentType)
	if err != nil {
		var srvErr *jenkins.ServerError
		var nfErr *jenkins.NotFoundError
		switch {
		case errors.As(err, &srvErr) && srvErr.StatusCode == http.StatusBadRequest:
			return nil, c.explainRejectedTrigger(jobPath, params, err)
		case len(params) > 0 && (errors.As(err, &nfErr) || errors.As(err, &srvErr) && srvErr.StatusCode == http.StatusMethodNotAllowed):
			if class, _, lookupErr := c.getJobClassAndParameters(jobPath); lookupErr == nil {
				switch job := (jenkins.Job{Class: class}); {
				case job.IsOrgFolder():
					return nil, fmt.Errorf("%s is an organization folder and takes no parameters; target a branch job (%s/<repo>/<branch>) to set parameters", jobPath, jobPath)
				case job.IsMultibranch():
					return nil, fmt.Errorf("%s is a multibranch project and takes no parameters; target a branch job (%s/<branch>) to set parameters", jobPath, jobPath)
				}
			}
		}
		return nil, fmt.Errorf("triggering build: %w", err)
	}
	defer CloseBody(resp)

	if indexing {
		return &TriggerResult{Indexing: true, OrgFolder: orgFolder}, nil
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, fmt.Errorf("no queue item returned — Jenkins did not provide a Location header")
	}
	// Parse queue item ID from Location header: .../queue/item/123/
	parts := strings.Split(strings.TrimRight(loc, "/"), "/")
	if len(parts) == 0 {
		return nil, fmt.Errorf("could not parse queue item from Location: %s", loc)
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return nil, fmt.Errorf("could not parse queue item ID from Location %q: %w", loc, err)
	}
	return &TriggerResult{QueueID: id}, nil
}

// explainRejectedTrigger turns a 400 from a trigger into advice. The common
// case is params sent to a job that has since dropped its definitions (e.g. a
// rebuild), which Jenkins rejects as "not parameterized".
func (c *Client) explainRejectedTrigger(jobPath string, params map[string]string, err error) error {
	if len(params) > 0 {
		if defs, lookupErr := c.GetJobParameters(jobPath); lookupErr == nil && len(defs) == 0 {
			return fmt.Errorf("build request rejected — %s takes no parameters; trigger it without them: %w", jobPath, err)
		}
	}
	return fmt.Errorf("build request rejected — check parameter names with 'jkit params %s': %w", jobPath, err)
}

func (c *Client) StopBuild(jobPath string, number int) error {
	path := fmt.Sprintf("%s/%d/stop", NormalizeJobPath(jobPath), number)
	resp, err := c.Post(path, nil, "")
	if err != nil {
		return fmt.Errorf("stopping build: %w", err)
	}
	CloseBody(resp)
	return nil
}

func consolePath(jobPath string, number int) string {
	return fmt.Sprintf("%s/%d/logText/progressiveText", NormalizeJobPath(jobPath), number)
}

// ConsoleLog follows a build's console from its first byte. See ProgressiveLog
// for how each poll finds where the last one stopped.
func (c *Client) ConsoleLog(jobPath string, number int) *ProgressiveLog {
	l := c.NewProgressiveLog(consolePath(jobPath, number), 0)
	l.explain = func(err error) error { return c.consoleErr(jobPath, err) }
	return l
}

func (c *Client) consoleErr(jobPath string, err error) error {
	if e := c.enrichNotFound(jobPath, err); e != err {
		return e
	}
	return fmt.Errorf("getting build log: %w", err)
}

// OpenConsoleText streams a build's whole console as it stands, console notes
// stripped and line ends as the build wrote them. A running build's text ends
// wherever the build is, possibly mid-line. The caller closes it.
func (c *Client) OpenConsoleText(jobPath string, number int) (io.ReadCloser, error) {
	resp, err := c.Get(fmt.Sprintf("%s/%d/consoleText", NormalizeJobPath(jobPath), number), nil)
	if err != nil {
		return nil, c.consoleErr(jobPath, err)
	}
	return resp.Body, nil
}

// GetBuildLogSize returns the X-Text-Size of the console log without
// downloading the body. For a complete log that is its stored size. For a
// running one it is the stored size on Jenkins 2.509 and later, and on earlier
// versions only the end of the first 10000 lines, so it is a lower bound.
func (c *Client) GetBuildLogSize(jobPath string, number int) (int64, error) {
	return c.progressiveSize(consolePath(jobPath, number))
}

// progressiveSize reads only the X-Text-Size header of a progressiveText
// endpoint.
func (c *Client) progressiveSize(path string) (int64, error) {
	resp, err := c.Get(path, url.Values{"start": {"0"}})
	if err != nil {
		return 0, fmt.Errorf("getting log size: %w", err)
	}
	// Close without draining: we only need the header, not the (potentially huge)
	// body. Closing aborts the transfer.
	defer func() { _ = resp.Body.Close() }()

	sz := resp.Header.Get("X-Text-Size")
	if sz == "" {
		return 0, fmt.Errorf("server did not report X-Text-Size")
	}
	n, err := strconv.ParseInt(sz, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing X-Text-Size %q: %w", sz, err)
	}
	return n, nil
}
