package jenkins

import "fmt"

// Trait and build-strategy classes store their configuration as opaque integers.
// The tables below translate them into the wording of the Jenkins UI dropdown
// that writes the value, taken from the plugin sources:
//
//	github-branch-source: src/main/java/org/jenkinsci/plugins/github_branch_source/
//	  BranchDiscoveryTrait.java (EXCLUDE_PRS=1, ONLY_PRS=2, ALL_BRANCHES=3)
//	  OriginPullRequestDiscoveryTrait.java, ForkPullRequestDiscoveryTrait.java
//	  (MERGE=1, HEAD=2, HEAD_AND_MERGE=3)
//
// An unknown id is never guessed at: it is reported raw next to the class name,
// because both traits fall through to a default that silently changes what gets
// discovered.

var branchDiscoveryStrategies = map[int]string{
	1: "branches that are not also filed as PRs",
	2: "only branches that are also filed as PRs",
	3: "all branches",
}

// prDiscoveryStrategies is shared by the origin and fork PR discovery traits:
// both decode strategyId with the same switch.
var prDiscoveryStrategies = map[int]string{
	1: "the PR merged with the current target branch revision",
	2: "the current PR head revision",
	3: "both the PR head revision and the merge with the current target branch",
}

func parseTraits(traits *xmlNode) []TraitInfo {
	var out []TraitInfo
	for _, t := range children(traits) {
		out = append(out, decodeTrait(t))
	}
	return out
}

// decodeTrait renders one SCM source trait. A class this code does not know is
// still returned, with Decoded false, so it appears in the output: a dropped
// trait would make the discovery report a lie. For a class it does know, any
// child element the switch below never looks at is listed in Unread, so a
// decoded line cannot look complete while an option it ignores is set.
func decodeTrait(n *xmlNode) TraitInfo {
	class := unescapeClass(n.name())
	short := shortClass(class)
	t := TraitInfo{Class: class, Name: humanize(short)}
	var read []string

	switch short {
	case "BranchDiscoveryTrait":
		t.Name = "Branch discovery"
		t.RawValue = n.text("strategyId")
		t.Meaning, t.Decoded = decodeStrategyID(t.RawValue, branchDiscoveryStrategies)
		read = []string{"strategyId"}
	case "OriginPullRequestDiscoveryTrait":
		t.Name = "Origin PR discovery"
		t.RawValue = n.text("strategyId")
		t.Meaning, t.Decoded = decodeStrategyID(t.RawValue, prDiscoveryStrategies)
		read = []string{"strategyId"}
	case "ForkPullRequestDiscoveryTrait":
		t.Name = "Fork PR discovery"
		t.RawValue = n.text("strategyId")
		t.Meaning, t.Decoded = decodeStrategyID(t.RawValue, prDiscoveryStrategies)
		if trust := n.child("trust"); trust != nil {
			t.Meaning += fmt.Sprintf("; trust %s", shortClass(unescapeClass(trust.attr("class"))))
		}
		read = []string{"strategyId", "trust"}
	case "TagDiscoveryTrait":
		t.Name = "Tag discovery"
		t.Meaning = "tags are discovered as buildable heads"
		t.Decoded = true
	case "WildcardSCMHeadFilterTrait":
		t.Name = "Head name filter"
		inc, exc := n.text("includes"), n.text("excludes")
		t.RawValue = "includes=" + inc + " excludes=" + exc
		switch {
		case inc == "":
			t.Meaning = "includes is empty, so no head matches and nothing is discovered"
		case exc == "":
			t.Meaning = fmt.Sprintf("only heads matching %q", inc)
		default:
			t.Meaning = fmt.Sprintf("only heads matching %q, except %q", inc, exc)
		}
		t.Decoded = true
		read = []string{"includes", "excludes"}
	case "RegexSCMHeadFilterTrait":
		t.Name = "Head name filter"
		t.RawValue = n.text("regex")
		t.Meaning = fmt.Sprintf("only heads matching regex %q", t.RawValue)
		t.Decoded = true
		read = []string{"regex"}
	case "RegexSCMSourceFilterTrait":
		// Organization navigators carry this one: it decides which repositories
		// of the organization become jobs at all.
		t.Name = "Repository name filter"
		t.RawValue = n.text("regex")
		t.Meaning = fmt.Sprintf("only repositories matching regex %q are indexed", t.RawValue)
		t.Decoded = true
		read = []string{"regex"}
	}
	if t.Decoded {
		t.Unread = unreadChildren(n, read...)
	}
	return t
}

func decodeStrategyID(raw string, table map[int]string) (string, bool) {
	id, ok := atoi(raw)
	if !ok {
		return "", false
	}
	meaning, known := table[id]
	if !known {
		return "", false
	}
	return meaning, true
}

// buildStrategies from basic-branch-build-strategies, keyed by class:
// src/main/java/jenkins/branch/buildstrategies/basic/. The label is the
// plugin's own display name; the meaning is its isAutomaticBuild() body.
//
// Sibling entries directly under <buildStrategies> are OR'd by branch-api
// (MultiBranchProject.java), and an empty list means "build anything but tags".
//
// The name SkipInitialBuildOnFirstBranchIndexing is misleading: it is not
// scoped to the job's first indexing. It returns true only when the head was
// already seen at a different revision, and branch-api passes a null
// last-seen revision for every newly discovered or re-adopted head, so it
// suppresses the first build of any branch whenever that branch appears.
var buildStrategyInfo = map[string]struct{ label, meaning string }{
	"SkipInitialBuildOnFirstBranchIndexing": {"Skip initial build on first branch indexing",
		"skip the first build of a head when it is first discovered (or rediscovered); build it on later changes"},
	"BranchBuildStrategyImpl":        {"Regular branches", "build heads that are neither PRs nor tags"},
	"TagBuildStrategyImpl":           {"Tags", "build tags"},
	"ChangeRequestBuildStrategyImpl": {"Change requests", "build change requests (PRs)"},
	"NamedBranchBuildStrategyImpl":   {"Named branches", "build non-PR, non-tag heads matching any of the filters below"},
	"AllBranchBuildStrategyImpl":     {"All strategies match", "every nested strategy must agree before a build starts"},
	"AnyBranchBuildStrategyImpl":     {"Any strategy matches", "one nested strategy agreeing is enough to start a build"},
	"NoneBranchBuildStrategyImpl":    {"No strategy matches", "build only when no nested strategy wants a build"},
}

// combinators nest other strategies. All three return false outright on an
// empty list, so an empty one blocks every build rather than imposing no
// constraint, which is how the bare label reads.
var combinators = map[string]bool{
	"AllBranchBuildStrategyImpl":  true,
	"AnyBranchBuildStrategyImpl":  true,
	"NoneBranchBuildStrategyImpl": true,
}

func parseBuildStrategies(bs *xmlNode) []BuildStrategy {
	var out []BuildStrategy
	for _, s := range children(bs) {
		out = append(out, decodeBuildStrategy(s))
	}
	return out
}

// decodeBuildStrategy renders one buildStrategies entry, recursing into the
// combinators. Unknown classes keep their name and are marked undecoded.
func decodeBuildStrategy(n *xmlNode) BuildStrategy {
	class := unescapeClass(n.name())
	short := shortClass(class)
	s := BuildStrategy{Class: class, Name: humanize(short)}
	info, known := buildStrategyInfo[short]
	if !known {
		return s
	}
	s.Name, s.Meaning, s.Decoded = info.label, info.meaning, true

	switch {
	case short == "TagBuildStrategyImpl":
		s.Meaning = tagMeaning(n)
		s.Unread = unreadChildren(n, "atLeastMillis", "atMostMillis")
	case short == "ChangeRequestBuildStrategyImpl":
		if isTrue(n.boolText("ignoreTargetOnlyChanges")) {
			s.Meaning += ", but not when the only change is the target branch moving"
		}
		if isTrue(n.boolText("ignoreUntrustedChanges")) {
			s.Meaning += ", and not when the newest change came from an untrusted author"
		}
		s.Unread = unreadChildren(n, "ignoreTargetOnlyChanges", "ignoreUntrustedChanges")
	case short == "NamedBranchBuildStrategyImpl":
		for _, f := range children(n.child("filters")) {
			s.Filters = append(s.Filters, decodeNameFilter(f))
		}
		if len(s.Filters) == 0 {
			s.Meaning = "no name filter is configured, so this strategy never starts a build"
		}
		s.Unread = unreadChildren(n, "filters")
	case combinators[short]:
		for _, c := range children(n.child("strategies")) {
			s.Children = append(s.Children, decodeBuildStrategy(c))
		}
		if len(s.Children) == 0 {
			s.Meaning = "no nested strategy is configured, so this strategy never starts a build"
		}
		s.Unread = unreadChildren(n, "strategies")
	default:
		s.Unread = unreadChildren(n)
	}
	return s
}

// tagMeaning renders TagBuildStrategyImpl's age bounds. -1 means unbounded, and
// a lower bound above the upper one makes isAutomaticBuild return false for
// every tag: the plugin's own comment calls it a configuration that corresponds
// to never building anything.
func tagMeaning(n *xmlNode) string {
	lo, hi := millisBound(n, "atLeastMillis"), millisBound(n, "atMostMillis")
	switch {
	case lo >= 0 && hi >= 0 && lo > hi:
		return fmt.Sprintf("no tag ever builds: the minimum age %s is above the maximum age %s",
			humanMillis(lo), humanMillis(hi))
	case lo >= 0 && hi >= 0:
		return fmt.Sprintf("build tags between %s and %s old", humanMillis(lo), humanMillis(hi))
	case lo >= 0:
		return "build tags at least " + humanMillis(lo) + " old"
	case hi >= 0:
		return "build tags at most " + humanMillis(hi) + " old"
	default:
		return "build tags of any age"
	}
}

// millisBound returns -1 for an absent or negative bound, both of which mean
// unbounded to the plugin.
func millisBound(n *xmlNode, field string) int {
	v := n.intText(field)
	if v == nil || *v < 0 {
		return -1
	}
	return *v
}

// Name filters are nested classes of NamedBranchBuildStrategyImpl, so XStream
// writes them as NamedBranchBuildStrategyImpl_-RegexNameFilter and the short
// class is the nested name alone.
func decodeNameFilter(n *xmlNode) NameFilter {
	class := unescapeClass(n.name())
	f := NameFilter{Class: class}
	var read []string

	switch shortClass(class) {
	case "ExactNameFilter":
		f.Meaning = fmt.Sprintf("name is exactly %q%s", n.text("name"), caseNote(n))
		f.Decoded = true
		read = []string{"name", "caseSensitive"}
	case "WildcardsNameFilter":
		inc, exc := n.text("includes"), n.text("excludes")
		switch {
		case inc == "":
			f.Meaning = "includes is empty, so this filter matches no name"
		case exc == "":
			f.Meaning = fmt.Sprintf("name matches %q%s", inc, caseNote(n))
		default:
			f.Meaning = fmt.Sprintf("name matches %q, except %q%s", inc, exc, caseNote(n))
		}
		f.Decoded = true
		read = []string{"includes", "excludes", "caseSensitive"}
	case "RegexNameFilter":
		f.Meaning = fmt.Sprintf("name matches regex %q%s", n.text("regex"), caseNote(n))
		f.Decoded = true
		read = []string{"regex", "caseSensitive"}
	}
	if f.Decoded {
		f.Unread = unreadChildren(n, read...)
	}
	return f
}

func caseNote(n *xmlNode) string {
	if v := n.boolText("caseSensitive"); v != nil && !*v {
		return ", ignoring case"
	}
	return ""
}

func isTrue(v *bool) bool { return v != nil && *v }
