package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// buildSourcesTree reads, in one request, the three actions that record which
// code a run executed: LibrariesAction (workflow-cps-global-lib) for the
// shared libraries a pipeline loaded, BuildData (git) for every repository the
// git plugin checked out, and SCMRevisionAction (scm-api) for the branch-source
// head the run was created from.
//
// buildsByBranchName is deliberately NOT requested. It accumulates one entry
// per branch ever built by the job and keeps them across builds, so a run can
// carry a branch recorded by a much older build number; only lastBuiltRevision
// belongs to the run being read.
//
// _class is named explicitly at every level because it is the only field whose
// absence carries information. Jenkins answers a tree query naming fields that
// do not exist with HTTP 200 and drops them silently, so an empty result never
// proves a field is absent — but an exported action always renders its _class,
// and an action with no exported properties renders as {}.
const buildSourcesTree = "actions[_class,libraries[name,version,trusted]," +
	"remoteUrls,lastBuiltRevision[SHA1,branch[name]],revision[_class,hash]]"

type buildSourcesPayload struct {
	Actions []struct {
		Class     string `json:"_class"`
		Libraries []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Trusted bool   `json:"trusted"`
		} `json:"libraries"`
		RemoteURLs        []string `json:"remoteUrls"`
		LastBuiltRevision *struct {
			SHA1   string `json:"SHA1"`
			Branch []struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"lastBuiltRevision"`
		Revision *struct {
			Class string `json:"_class"`
			Hash  string `json:"hash"`
		} `json:"revision"`
	} `json:"actions"`
}

// Expected owners of each field. An action carrying the data under a different
// class is still read, but says so: these are plugin classes, and a subclass or
// a replacement plugin can export the same shape with different semantics.
const (
	librariesActionClass   = "LibrariesAction"
	buildDataActionClass   = "BuildData"
	scmRevisionActionClass = "SCMRevisionAction"
)

// GetBuildSources reports which code a build ran: the branch-source revision,
// every git checkout with its commit, and every shared library with the ref it
// asked for joined to the commit that ref resolved to.
func (c *Client) GetBuildSources(jobPath string, number int) (*jenkins.BuildSources, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"tree": {buildSourcesTree}})
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting build sources: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload buildSourcesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding build sources: %w", err)
	}

	src := &jenkins.BuildSources{Job: jobPath, Build: number}
	for _, a := range payload.Actions {
		if strings.Contains(a.Class, librariesActionClass) {
			src.LibrariesActionPresent = true
		}

		if len(a.Libraries) > 0 {
			src.Warnings = appendUnexpectedClass(src.Warnings, a.Class, librariesActionClass, "shared libraries")
			for _, l := range a.Libraries {
				src.Libraries = append(src.Libraries, jenkins.SharedLibrary{
					Name: l.Name, Version: l.Version, Trusted: l.Trusted,
				})
			}
		}

		if a.LastBuiltRevision != nil || len(a.RemoteURLs) > 0 {
			src.Warnings = appendUnexpectedClass(src.Warnings, a.Class, buildDataActionClass, "a git checkout")
			co := jenkins.GitCheckout{ActionClass: a.Class, RemoteURLs: a.RemoteURLs}
			if a.LastBuiltRevision != nil {
				co.SHA1 = a.LastBuiltRevision.SHA1
				for _, b := range a.LastBuiltRevision.Branch {
					co.Branches = append(co.Branches, b.Name)
				}
			}
			src.Checkouts = append(src.Checkouts, co)
		}

		if a.Revision != nil {
			src.Warnings = appendUnexpectedClass(src.Warnings, a.Class, scmRevisionActionClass, "a pipeline revision")
			src.PipelineRevisions = append(src.PipelineRevisions, jenkins.PipelineRevision{
				ActionClass: a.Class, RevisionClass: a.Revision.Class, Hash: a.Revision.Hash,
			})
		}
	}

	if len(src.PipelineRevisions) > 1 {
		src.Warnings = append(src.Warnings, fmt.Sprintf(
			"the build carries %d SCMRevisionActions, so no single one is 'the' revision the pipeline was read at",
			len(src.PipelineRevisions)))
	}

	jenkins.ResolveLibrarySHAs(src.Libraries, src.Checkouts)
	return src, nil
}

// appendUnexpectedClass records that a field was read from an action whose
// class is not the one that normally exports it. Dropping such an action would
// silently shorten the report; trusting it without saying so would hide that
// the reading rests on an assumption.
func appendUnexpectedClass(warnings []string, class, want, what string) []string {
	if strings.Contains(class, want) {
		return warnings
	}
	name := class
	if name == "" {
		name = "(no _class)"
	}
	return append(warnings, fmt.Sprintf("read %s from %s, which is not a %s", what, name, want))
}
