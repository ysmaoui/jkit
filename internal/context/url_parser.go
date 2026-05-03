package context

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParsedURL holds the components extracted from a Jenkins URL.
type ParsedURL struct {
	Host        string // scheme + host + port, e.g. "https://jenkins.prod.com"
	JobPath     string // "team/svc" (not normalized with /job/ segments)
	BuildNumber int    // 0 if no build number in URL
	IsURL       bool   // always true when parsed from a URL
}

// ParseJenkinsURL extracts job path, build number, and host from a Jenkins URL.
// Supports classic /job/ URLs and Blue Ocean /blue/ URLs.
func ParseJenkinsURL(raw string) (*ParsedURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q — expected http or https", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in URL")
	}

	host := u.Scheme + "://" + u.Host

	// Use EscapedPath to preserve %2F in Blue Ocean URLs;
	// u.Path decodes %2F to / which breaks segment splitting.
	path := u.EscapedPath()

	// Blue Ocean URL
	if strings.HasPrefix(path, "/blue/") {
		return parseBlueOcean(host, path)
	}

	// Classic /job/ URL
	if strings.Contains(path, "/job/") {
		return parseClassic(host, path)
	}

	return nil, fmt.Errorf("not a Jenkins URL (no /job/ or /blue/ path)")
}

// unescapeJobSegment decodes percent-encoding in a job/branch name segment
// while preserving %2F. A segment from EscapedPath split by "/" never contains
// a literal "/", so any "/" after PathUnescape came from %2F and represents a
// slash within a branch/job name (e.g. "feature/foo"). We re-encode those to
// %2F so NormalizeJobPath can later distinguish name-internal slashes from
// path separators.
func unescapeJobSegment(s string) string {
	decoded, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return strings.ReplaceAll(decoded, "/", "%2F")
}

func parseClassic(host, path string) (*ParsedURL, error) {
	segments := strings.Split(strings.Trim(path, "/"), "/")

	var jobParts []string
	buildNum := 0

	for i := 0; i < len(segments); i++ {
		if segments[i] == "job" && i+1 < len(segments) {
			i++
			decoded := unescapeJobSegment(segments[i])
			jobParts = append(jobParts, decoded)
		} else if len(jobParts) > 0 {
			// First non-job segment after job parts — check if it's a build number
			if n, err := strconv.Atoi(segments[i]); err == nil {
				buildNum = n
			}
			// Stop collecting — anything else (console, artifact, etc.) is ignored
			break
		}
	}

	if len(jobParts) == 0 {
		return nil, fmt.Errorf("empty job path")
	}

	return &ParsedURL{
		Host:        host,
		JobPath:     strings.Join(jobParts, "/"),
		BuildNumber: buildNum,
		IsURL:       true,
	}, nil
}

func parseBlueOcean(host, path string) (*ParsedURL, error) {
	// Pattern: /blue/organizations/jenkins/{encodedJobPath}/detail/{encodedJobPath}/{buildNum}/...
	// Or:      /blue/organizations/jenkins/{encodedJobPath}/activity
	segments := strings.Split(strings.Trim(path, "/"), "/")

	// Minimum: blue/organizations/jenkins/{jobPath}/...
	if len(segments) < 4 {
		return nil, fmt.Errorf("invalid Blue Ocean URL")
	}
	// segments[0]="blue", [1]="organizations", [2]="jenkins", [3]=encodedJobPath
	encodedJobPath := segments[3]
	decoded, err := url.PathUnescape(encodedJobPath)
	if err != nil {
		return nil, fmt.Errorf("decoding Blue Ocean job path: %w", err)
	}
	if decoded == "" {
		return nil, fmt.Errorf("empty job path")
	}

	buildNum := 0
	jobPath := decoded

	// Look for detail/{branchOrPath}/{buildNum} pattern
	for i := 4; i < len(segments); i++ {
		if segments[i] == "detail" && i+1 < len(segments) {
			// For regular pipelines: detail/{pipelineName}/{buildNum}
			// For multibranch pipelines: detail/{branchName}/{buildNum}
			// Blue Ocean uses the full encoded path for simple pipelines
			// but only the last component for deeply nested ones.
			// Branch names with %2F (e.g. feature%2Fbranch) must preserve
			// the encoding so NormalizeJobPath doesn't split on them.
			detailSeg := segments[i+1]
			isSamePipeline := detailSeg == encodedJobPath
			if !isSamePipeline {
				// Check if detail segment matches last component of decoded path
				// (Blue Ocean uses just the pipeline name for nested pipelines)
				lastComponent := decoded
				if idx := strings.LastIndex(decoded, "/"); idx >= 0 {
					lastComponent = decoded[idx+1:]
				}
				isSamePipeline = unescapeJobSegment(detailSeg) == lastComponent
			}
			if !isSamePipeline {
				branchName := unescapeJobSegment(detailSeg)
				jobPath = decoded + "/" + branchName
			}
			if i+2 < len(segments) {
				if n, err := strconv.Atoi(segments[i+2]); err == nil {
					buildNum = n
				}
			}
			break
		}
	}

	return &ParsedURL{
		Host:        host,
		JobPath:     jobPath,
		BuildNumber: buildNum,
		IsURL:       true,
	}, nil
}
