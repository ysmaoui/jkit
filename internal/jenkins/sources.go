package jenkins

import (
	"fmt"
	"strings"
)

// Library resolution outcomes. The distinction is the point of the whole
// report: a caller acting on a SHA must be able to tell what kind of evidence
// produced it, because only one of these is a direct record of the commit.
const (
	// LibraryPinned means the requested version is itself a commit id, so no
	// join was needed and nothing can move underneath it.
	LibraryPinned = "pinned"
	// LibraryMatchedBranch means the SHA comes from a git checkout whose
	// recorded branch name equals the requested version.
	LibraryMatchedBranch = "matched-by-branch"
	// LibraryUnresolved means no SHA can be attributed without guessing.
	LibraryUnresolved = "unresolved"
)

// SharedLibrary is one entry of a build's LibrariesAction, joined where
// possible to the checkout that produced it.
//
// Version is the ref the pipeline ASKED for, which is all LibrariesAction
// records. SHA1 is the commit that ref turned out to be, and it is only ever
// filled from evidence, never inferred from a repository name.
type SharedLibrary struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Trusted    bool   `json:"trusted"`
	SHA1       string `json:"sha1,omitempty"`
	RemoteURL  string `json:"remoteUrl,omitempty"`
	Resolution string `json:"resolution"`
	// Reason is set only when no commit could be attributed, and says which of
	// the several different gaps this one is.
	Reason string `json:"reason,omitempty"`
}

// Moving reports whether the library was requested by a name that can point at
// a different commit on the next build.
func (l SharedLibrary) Moving() bool { return l.Resolution != LibraryPinned }

// GitCheckout is one hudson.plugins.git.util.BuildData on the build: a
// repository the git plugin checked out, and the revision it ended on.
//
// Only lastBuiltRevision is read. BuildData also carries buildsByBranchName,
// which ACCUMULATES entries across builds of the same job: a build can carry a
// branch entry recorded by a much earlier build number, so reading it reports
// code that this build never ran.
type GitCheckout struct {
	ActionClass string   `json:"actionClass"`
	RemoteURLs  []string `json:"remoteUrls,omitempty"`
	Branches    []string `json:"branches,omitempty"`
	SHA1        string   `json:"sha1,omitempty"`
	// Library names the shared library this checkout was attributed to, empty
	// when it belongs to something else or could not be attributed.
	Library string `json:"library,omitempty"`
}

// Remote renders the checkout's origin. A BuildData records every remote
// configured for the checkout, which is normally one.
func (c GitCheckout) Remote() string { return strings.Join(c.RemoteURLs, ", ") }

// PipelineRevision is a jenkins.scm.api.SCMRevisionAction: the branch-source
// head the run was created from, which is the revision the Jenkinsfile itself
// was read at on a multibranch job.
type PipelineRevision struct {
	ActionClass   string `json:"actionClass"`
	RevisionClass string `json:"revisionClass"`
	Hash          string `json:"hash,omitempty"`
}

// BuildSources is everything one build recorded about the code it ran.
//
// LibrariesActionPresent separates "this build loaded no shared library" from
// "the libraries could not be read", which look identical in an empty list and
// mean opposite things to someone chasing a change in behaviour.
type BuildSources struct {
	Job                    string             `json:"job"`
	Build                  int                `json:"build"`
	PipelineRevisions      []PipelineRevision `json:"pipelineRevisions"`
	Libraries              []SharedLibrary    `json:"libraries"`
	Checkouts              []GitCheckout      `json:"checkouts"`
	LibrariesActionPresent bool               `json:"librariesActionPresent"`
	// Warnings names data the parser read from somewhere it did not expect, so
	// a report built on an unfamiliar plugin class is never silently trusted.
	Warnings []string `json:"warnings,omitempty"`
}

// MovingLibraries counts libraries whose requested version is not a commit id.
func (s *BuildSources) MovingLibraries() int {
	n := 0
	for _, l := range s.Libraries {
		if l.Moving() {
			n++
		}
	}
	return n
}

// ResolveLibrarySHAs attributes a commit to each library, in place.
//
// The only join key available is the branch name: LibrariesAction records a
// name and a ref and never a SHA, BuildData records a SHA and a branch name and
// never a library name. So the join is a heuristic, and it is applied only
// where it cannot be wrong:
//
//   - a version that is already a commit id needs no join;
//   - two libraries on the same version make every checkout ambiguous between
//     them, so neither gets a SHA;
//   - branch names are compared verbatim. A checkout recorded as
//     "refs/remotes/origin/develop" is NOT treated as "develop", because
//     normalising ref names would also merge a pipeline checkout of an
//     unrelated repository into the library's evidence;
//   - several checkouts on one branch name resolve only when they agree on
//     both commit and remote, i.e. when the answer does not depend on which one
//     was the library's.
//
// Everything else stays unresolved with the reason recorded. The command's
// value is being trusted about which code ran, and a guessed SHA is worse than
// no SHA.
func ResolveLibrarySHAs(libs []SharedLibrary, checkouts []GitCheckout) {
	perVersion := map[string]int{}
	for _, l := range libs {
		perVersion[l.Version]++
	}

	for i := range libs {
		lib := &libs[i]
		if isCommitID(lib.Version) {
			lib.SHA1 = strings.ToLower(lib.Version)
			lib.Resolution = LibraryPinned
			continue
		}

		lib.Resolution = LibraryUnresolved
		switch {
		case len(checkouts) == 0:
			lib.Reason = "the build recorded no git checkout at all"
			continue
		case perVersion[lib.Version] > 1:
			lib.Reason = fmt.Sprintf("%d libraries request %q, so no checkout can be attributed to one of them",
				perVersion[lib.Version], lib.Version)
			continue
		}

		matches := checkoutsOnBranch(checkouts, lib.Version)
		if len(matches) == 0 {
			lib.Reason = fmt.Sprintf("no git checkout recorded the branch %q", lib.Version)
			continue
		}
		if !sameRevision(checkouts, matches) {
			lib.Reason = fmt.Sprintf("%d different checkouts recorded the branch %q", len(matches), lib.Version)
			continue
		}

		first := &checkouts[matches[0]]
		lib.SHA1 = first.SHA1
		lib.RemoteURL = first.Remote()
		lib.Resolution = LibraryMatchedBranch
		for _, idx := range matches {
			checkouts[idx].Library = lib.Name
		}
	}
}

// checkoutsOnBranch returns the indices of checkouts whose recorded revision
// sits on the given branch name. A revision can be recorded under several
// names when one commit is the head of more than one branch.
func checkoutsOnBranch(checkouts []GitCheckout, branch string) []int {
	var out []int
	for i, c := range checkouts {
		if c.SHA1 == "" {
			continue
		}
		for _, b := range c.Branches {
			if b == branch {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// sameRevision reports whether every candidate names the same commit in the
// same repository, which makes the choice between them immaterial.
func sameRevision(checkouts []GitCheckout, idx []int) bool {
	first := checkouts[idx[0]]
	for _, i := range idx[1:] {
		if checkouts[i].SHA1 != first.SHA1 || checkouts[i].Remote() != first.Remote() {
			return false
		}
	}
	return true
}

// isCommitID recognises a full git object name. Abbreviated hashes are not
// accepted: a short hex string is also a legal branch name, and the whole point
// here is to claim a commit only when the version cannot be anything else.
func isCommitID(v string) bool {
	if len(v) != 40 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
