package jenkins

import (
	"regexp"
	"strings"
)

// LibraryEvidence is what the console log recorded about one shared library's
// resolution: the repository the git plugin asked, the ref it asked for, and
// the commit that ref turned out to be.
//
// Unlike the LibrariesAction/BuildData join in ResolveLibrarySHAs, this is a
// direct record of what happened during the build rather than an attribution
// made afterwards. It is also build-time truth: the library's configured
// repository can be repointed later, which would make a join through the
// current configuration attribute the wrong checkout.
type LibraryEvidence struct {
	RemoteURL string
	Ref       string
	SHA1      string
	// Conflict is set when the log resolved the same library to more than one
	// commit. Loading a library twice at different commits is legal, and taking
	// the first would report code the rest of the build did not run.
	Conflict bool
}

// libraryKey identifies a library by the name and version the pipeline asked
// for, which is what LibrariesAction records.
type libraryKey struct{ Name, Version string }

// The three lines that carry the resolution. Emitted by the library retriever
// in workflow-cps-global-lib and by git-plugin underneath it; a build's own SCM
// checkout produces none of them, which is what keeps the evidence scoped to
// libraries.
var (
	// The name is anchored to the first '@' because that character is the
	// name/version separator in @Library("name@version"), so a name cannot
	// contain one — but a git ref can, and a greedy split would move the tail
	// of such a ref into the name.
	loadingLibraryRe = regexp.MustCompile(`^Loading library ([^@]+)@(.+)$`)
	lsRemoteRe       = regexp.MustCompile(`^\s*> git ls-remote -- (\S+)`)
	foundMatchRe     = regexp.MustCompile(`^Found match: (\S+) revision ([0-9a-fA-F]{40})$`)

	// Timestamper's "prepend to console" mode writes an ISO-8601 stamp into the
	// text that logText/progressiveText serves, so console lines routinely
	// arrive as "[2026-09-13T19:00:07.813Z] Loading library x@main". It is
	// stripped for matching only, never from anything this package emits, so a
	// line whose own content begins with a bracketed timestamp is not altered —
	// at worst it is examined twice.
	consoleStampRe = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2}T[0-9:.]+Z?\]\s`)
)

// LibraryLogScanner reads console lines one at a time and records how each
// shared library resolved. It is fed line by line so a caller can stream a
// multi-hundred-megabyte console with bounded memory and stop early.
//
// A block runs from "Loading library name@version" to the "Found match" that
// closes it. Closing clears the open block, so a later "Found match" belonging
// to something else is never attributed to the library above it. A block that
// never reaches "Found match" contributes nothing rather than borrowing the
// next block's evidence.
type LibraryLogScanner struct {
	open       libraryKey
	openActive bool
	pendingURL string
	evidence   map[libraryKey]LibraryEvidence
}

func NewLibraryLogScanner() *LibraryLogScanner {
	return &LibraryLogScanner{evidence: map[libraryKey]LibraryEvidence{}}
}

// Line feeds one console line, with any timestamp prefix already removed.
func (s *LibraryLogScanner) Line(line string) {
	line = consoleStampRe.ReplaceAllString(strings.TrimRight(line, "\r"), "")

	if m := loadingLibraryRe.FindStringSubmatch(line); m != nil {
		s.open = libraryKey{Name: m[1], Version: m[2]}
		s.openActive = true
		s.pendingURL = ""
		return
	}
	if !s.openActive {
		return
	}
	if m := lsRemoteRe.FindStringSubmatch(line); m != nil {
		s.pendingURL = m[1]
		return
	}
	if m := foundMatchRe.FindStringSubmatch(line); m != nil {
		s.record(s.open, LibraryEvidence{RemoteURL: s.pendingURL, Ref: m[1], SHA1: strings.ToLower(m[2])})
		s.openActive = false
		s.pendingURL = ""
	}
}

func (s *LibraryLogScanner) record(key libraryKey, got LibraryEvidence) {
	prev, seen := s.evidence[key]
	if !seen {
		s.evidence[key] = got
		return
	}
	if prev.Conflict || prev.SHA1 == got.SHA1 {
		return
	}
	prev.Conflict = true
	s.evidence[key] = prev
}

// Evidence returns what the log recorded for one library, and whether anything
// was recorded at all.
func (s *LibraryLogScanner) Evidence(name, version string) (LibraryEvidence, bool) {
	e, ok := s.evidence[libraryKey{Name: name, Version: version}]
	return e, ok
}

// A caller must feed the WHOLE console, not stop once the libraries it wants
// have appeared. A library can be loaded again later in the build at a
// different commit, and that second load is only visible by reading on; a
// scanner stopped at the first hit would report the earlier commit as though it
// were the answer. Reading the whole log is the price of the Conflict flag
// meaning anything.
