package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	appctx "github.com/ysmaoui/jkit/internal/context"
	"github.com/ysmaoui/jkit/internal/jenkins"
)

type executorSlot struct {
	CurrentExecutable *struct {
		URL       string `json:"url"`
		Timestamp int64  `json:"timestamp"`
	} `json:"currentExecutable"`
}

// RunningBuilds lists the builds executing anywhere on the instance, oldest
// first, with builds of unknown start last. Pipeline builds run on flyweight
// oneOffExecutors; a build holding an agent through node{} or a freestyle build
// shows under executors. A pipeline inside node{} is in both, so builds are
// deduplicated by URL.
func (c *Client) RunningBuilds() ([]jenkins.RunningBuild, error) {
	const exe = "currentExecutable[url,timestamp]"
	resp, err := c.Get("/computer/api/json", url.Values{
		"tree": {"computer[oneOffExecutors[" + exe + "],executors[" + exe + "]]"},
	})
	if err != nil {
		var pe *jenkins.PermissionError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("cannot read the executor list on %s: some instances need more than Overall/Read for /computer, such as an Agent permission; ask a Jenkins admin: %w", c.host, err)
		}
		return nil, fmt.Errorf("getting executors: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Computer []struct {
			OneOffExecutors []executorSlot `json:"oneOffExecutors"`
			Executors       []executorSlot `json:"executors"`
		} `json:"computer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding executors: %w", err)
	}

	seen := map[string]bool{}
	builds := []jenkins.RunningBuild{}
	add := func(slots []executorSlot) {
		for _, s := range slots {
			ex := s.CurrentExecutable
			if ex == nil || ex.URL == "" || seen[ex.URL] {
				continue
			}
			seen[ex.URL] = true
			// Branch indexing and other non-run work also occupies flyweight
			// executors; only a URL naming a job and a build number is a build.
			p, err := appctx.ParseJenkinsURL(ex.URL)
			if err != nil || p.BuildNumber <= 0 {
				continue
			}
			builds = append(builds, jenkins.RunningBuild{
				Job: p.JobPath, Number: p.BuildNumber, URL: ex.URL, Timestamp: ex.Timestamp,
			})
		}
	}
	for _, comp := range result.Computer {
		add(comp.OneOffExecutors)
		add(comp.Executors)
	}
	sort.SliceStable(builds, func(i, j int) bool {
		ti, tj := builds[i].Timestamp, builds[j].Timestamp
		if (ti <= 0) != (tj <= 0) {
			return tj <= 0
		}
		return ti < tj
	})
	return builds, nil
}

// FilterRunningByFolder keeps builds whose job is folder or lies under it. The
// match is by whole path segments, so "team" does not match "team-b/app".
func FilterRunningByFolder(builds []jenkins.RunningBuild, folder string) []jenkins.RunningBuild {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return builds
	}
	out := []jenkins.RunningBuild{}
	for _, b := range builds {
		if b.Job == folder || strings.HasPrefix(b.Job, folder+"/") {
			out = append(out, b)
		}
	}
	return out
}
