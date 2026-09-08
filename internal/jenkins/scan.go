package jenkins

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A branch-indexing log is prose written by the SCM source plugin, not a data
// format: there is no /indexing/api/json behind it. Everything below reads that
// prose, so it is a heuristic by construction. Two rules keep it from lying:
// a line it does not recognize is carried through verbatim instead of dropped,
// and the log's own "N branches were processed" totals are compared against the
// blocks found so an incomplete parse announces itself.

// ScanWording names the provider wording this parser was built and tested
// against. Bitbucket, GitLab and plain-git sources word the same verdicts
// differently and will read as unrecognized here.
const ScanWording = "GitHub branch source (github-branch-source)"

// Criteria verdicts. Unknown means the block carried no line this parser
// recognizes as a verdict, not that the head was rejected.
const (
	CriteriaMet     = "met"
	CriteriaNotMet  = "not met"
	CriteriaUnknown = "unrecognized"
)

// scanTimeLayout matches the stamp branch-api writes on the first and last
// lines: [Tue Sep 08 14:58:10 UTC 2026].
const scanTimeLayout = "Mon Jan 02 15:04:05 MST 2006"

var (
	scanHeadRe      = regexp.MustCompile(`^Checking (branch|tag|pull request) (.+)$`)
	scanStartRe     = regexp.MustCompile(`^\[(.+)\] Starting branch indexing`)
	scanEndRe       = regexp.MustCompile(`^\[(.+)\] Finished branch indexing`)
	scanResultRe    = regexp.MustCompile(`^Finished: (\S+)$`)
	scanExamineRe   = regexp.MustCompile(`^Examining (.+)$`)
	scanDoneRe      = regexp.MustCompile(`^Finished examining (.+)$`)
	scanProcessedRe = regexp.MustCompile(`^(\d+) (branch|branches|tag|tags|pull request|pull requests) were processed$`)
	// The section markers vary by source: "Checking pull-requests..." on one,
	// "Getting remote pull requests..." on another.
	scanSectionRe = regexp.MustCompile(`^(Checking|Getting) [a-z -]+\.\.\.$`)
	scanConnectRe = regexp.MustCompile(`^Connecting to `)
	// The script name is configurable, and the plugin quotes it with typographic
	// quotes, so both the name and the quote characters are matched loosely.
	scanScriptRe = regexp.MustCompile(`^[\x60'"\x{2018}\x{201C}].+[\x60'"\x{2019}\x{201D}] (not )?found$`)
	scanActionRe = regexp.MustCompile(`^(No changes detected|Changes detected|Scheduled build for|No automatic build triggered for|Met criteria|Does not meet criteria)`)
)

// ScanHead is one "Checking branch X" block: the head that was examined and
// every line the log wrote about it.
type ScanHead struct {
	Kind         string   `json:"kind"` // branch | tag | pull request
	Name         string   `json:"name"`
	Repo         string   `json:"repo,omitempty"`
	Criteria     string   `json:"criteria"`
	Reason       string   `json:"reason,omitempty"`  // e.g. "'Jenkinsfile' not found"
	Actions      []string `json:"actions,omitempty"` // what happened after the verdict
	Unrecognized []string `json:"unrecognized,omitempty"`
	Lines        []string `json:"lines"` // the block verbatim, its "Checking" line first
}

// ScanLog is the parse of one indexing run. Every field is best-effort except
// Lines on each head, which is verbatim.
type ScanLog struct {
	BestEffort bool   `json:"bestEffort"`
	Wording    string `json:"parsedFrom"`

	Cause      string     `json:"cause,omitempty"`
	StartedRaw string     `json:"startedRaw,omitempty"`
	Started    *time.Time `json:"started,omitempty"`
	Finished   bool       `json:"finished"`
	Result     string     `json:"result,omitempty"`
	Repos      []string   `json:"repos,omitempty"`
	Heads      []ScanHead `json:"heads"`

	// Reported holds the log's own totals ("6 branches were processed"), keyed
	// by singular kind. Parsed holds what this parser found. A disagreement
	// means the summary is incomplete.
	Reported map[string]int `json:"reportedCounts,omitempty"`
	Parsed   map[string]int `json:"parsedCounts,omitempty"`

	// Stray holds lines outside every head block that the parser does not
	// recognize, so nothing the log said disappears from the summary.
	Stray []string `json:"stray,omitempty"`
}

// ParseScanLog reads an indexing console log.
func ParseScanLog(text string) *ScanLog {
	l := &ScanLog{
		BestEffort: true,
		Wording:    ScanWording,
		Reported:   map[string]int{},
		Parsed:     map[string]int{},
	}

	var repo string
	var head *ScanHead
	closeHead := func() {
		if head != nil {
			l.Heads = append(l.Heads, *head)
			l.Parsed[head.Kind]++
			head = nil
		}
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		if m := scanHeadRe.FindStringSubmatch(trimmed); m != nil {
			closeHead()
			head = &ScanHead{
				Kind:     m[1],
				Name:     m[2],
				Repo:     repo,
				Criteria: CriteriaUnknown,
				Lines:    []string{line},
			}
			continue
		}

		if l.structural(trimmed, &repo) {
			closeHead()
			continue
		}

		if head != nil {
			head.Lines = append(head.Lines, line)
			classify(head, trimmed)
			continue
		}
		if trimmed != "" {
			if l.Cause == "" && l.StartedRaw == "" {
				l.Cause = trimmed
				continue
			}
			l.Stray = append(l.Stray, trimmed)
		}
	}
	closeHead()

	for i := range l.Heads {
		trimBlankTail(&l.Heads[i])
	}
	return l
}

// structural reports whether the line is part of the run's frame rather than a
// verdict about a head, recording what it carries.
func (l *ScanLog) structural(trimmed string, repo *string) bool {
	switch {
	case trimmed == "":
		return false
	case scanStartRe.MatchString(trimmed):
		m := scanStartRe.FindStringSubmatch(trimmed)
		l.StartedRaw = m[1]
		if t, ok := parseScanTime(m[1]); ok {
			l.Started = &t
		}
		return true
	case scanResultRe.MatchString(trimmed):
		l.Result = scanResultRe.FindStringSubmatch(trimmed)[1]
		l.Finished = true
		return true
	case scanExamineRe.MatchString(trimmed):
		*repo = scanExamineRe.FindStringSubmatch(trimmed)[1]
		l.Repos = append(l.Repos, *repo)
		return true
	case scanProcessedRe.MatchString(trimmed):
		m := scanProcessedRe.FindStringSubmatch(trimmed)
		n, _ := strconv.Atoi(m[1])
		l.Reported[singularKind(m[2])] += n
		return true
	case scanEndRe.MatchString(trimmed), scanDoneRe.MatchString(trimmed),
		scanSectionRe.MatchString(trimmed), scanConnectRe.MatchString(trimmed):
		return true
	}
	return false
}

// classify reads one line inside a head's block. A line that matches nothing is
// kept under Unrecognized: on an untested provider that is the signal that the
// verdict columns are missing something, and it is printed rather than dropped.
func classify(h *ScanHead, trimmed string) {
	switch {
	case trimmed == "":
	case trimmed == "Met criteria":
		h.Criteria = CriteriaMet
	case trimmed == "Does not meet criteria":
		h.Criteria = CriteriaNotMet
	case scanScriptRe.MatchString(trimmed):
		h.Reason = trimmed
	case scanActionRe.MatchString(trimmed):
		h.Actions = append(h.Actions, trimmed)
	default:
		h.Unrecognized = append(h.Unrecognized, trimmed)
	}
}

func trimBlankTail(h *ScanHead) {
	for len(h.Lines) > 0 && strings.TrimSpace(h.Lines[len(h.Lines)-1]) == "" {
		h.Lines = h.Lines[:len(h.Lines)-1]
	}
}

func singularKind(p string) string {
	switch p {
	case "branches":
		return "branch"
	case "tags":
		return "tag"
	case "pull requests":
		return "pull request"
	}
	return p
}

// parseScanTime reads the stamp on the "Starting branch indexing" line. Go
// resolves an unknown zone abbreviation to a zero offset, which would shift the
// age silently by the real offset, so an unresolvable zone reports failure and
// the caller falls back to showing the stamp verbatim with no age.
func parseScanTime(raw string) (time.Time, bool) {
	t, err := time.Parse(scanTimeLayout, raw)
	if err != nil {
		return time.Time{}, false
	}
	if name, offset := t.Zone(); offset == 0 && name != "UTC" && name != "GMT" {
		return time.Time{}, false
	}
	return t, true
}

// Age returns how long ago the scan started, and whether that is known at all.
func (l *ScanLog) Age(now time.Time) (time.Duration, bool) {
	if l.Started == nil {
		return 0, false
	}
	return now.Sub(*l.Started), true
}

// Find returns the blocks for a head of the given name, across every kind and
// repository. A tag and a branch can share a name, and an organization folder
// scans the same branch name in several repositories, so this returns a list.
func (l *ScanLog) Find(name string) []ScanHead {
	var out []ScanHead
	for _, h := range l.Heads {
		if h.Name == name {
			out = append(out, h)
		}
	}
	return out
}

// Similar returns head names that contain the query, for a "not in this scan"
// message that can point at a near miss (a tag of the same name, a branch whose
// prefix was mistyped).
func (l *ScanLog) Similar(name string) []string {
	q := strings.ToLower(name)
	seen := map[string]bool{}
	var out []string
	for _, h := range l.Heads {
		if h.Name == name || !strings.Contains(strings.ToLower(h.Name), q) {
			continue
		}
		if !seen[h.Name] {
			seen[h.Name] = true
			out = append(out, h.Name)
		}
	}
	return out
}

// CountDisagreements compares the log's own per-kind totals with the blocks
// parsed out of it. A non-empty result means the summary is missing heads and
// must not be read as complete.
func (l *ScanLog) CountDisagreements() []string {
	var out []string
	for kind, reported := range l.Reported {
		if parsed := l.Parsed[kind]; parsed != reported {
			out = append(out, PluralHeads(kind, reported)+" reported by the log, "+strconv.Itoa(parsed)+" parsed here")
		}
	}
	sort.Strings(out)
	return out
}

// UnrecognizedHeads counts heads whose block carried no verdict this parser
// knows.
func (l *ScanLog) UnrecognizedHeads() int {
	n := 0
	for _, h := range l.Heads {
		if h.Criteria == CriteriaUnknown {
			n++
		}
	}
	return n
}

// PluralHeads renders a count of heads of one kind ("4 branches"). "branch"
// does not take a bare "s", and the kinds come from the log's own vocabulary.
func PluralHeads(kind string, n int) string {
	noun := kind
	if n != 1 {
		switch kind {
		case "branch":
			noun = "branches"
		case "pull request":
			noun = "pull requests"
		default:
			noun = kind + "s"
		}
	}
	return strconv.Itoa(n) + " " + noun
}
