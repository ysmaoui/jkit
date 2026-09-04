package jenkins

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Source kinds. A branch source and a navigator are configured on a container
// and decide what it discovers; a checkout is the record a branch child keeps
// of where it was cloned from, and carries no discovery rules of its own.
const (
	SourceBranch    = "branch source"
	SourceNavigator = "navigator"
	SourceCheckout  = "checkout"
)

// JobDefinition is the debugger-facing view of a job's config.xml: which
// Jenkinsfile runs, where it comes from, which heads the SCM source discovers,
// and how builds are retained.
type JobDefinition struct {
	JobPath     string           `json:"jobPath,omitempty"`
	Class       string           `json:"class"`
	Kind        string           `json:"kind"`
	Container   bool             `json:"container"`
	DisplayName string           `json:"displayName,omitempty"`
	Description string           `json:"description,omitempty"`
	Disabled    *bool            `json:"disabled,omitempty"`
	Script      *ScriptSource    `json:"script,omitempty"`
	Sources     []BranchSource   `json:"sources,omitempty"`
	Triggers    []TriggerInfo    `json:"triggers,omitempty"`
	Retention   *RetentionPolicy `json:"retention,omitempty"`
	Parent      string           `json:"parent,omitempty"`

	// branchChild records that config.xml carried a BranchJobProperty. Kind is
	// a display string, so keying anything off it would break on a reword.
	branchChild bool
}

// BranchSource is one repository or organization a container indexes, together
// with the traits that decide which heads it discovers and the strategies that
// decide when a discovered head builds. A multibranch job can carry several,
// and an organization folder carries navigators whose traits every child job
// inherits, so all of them are kept: reporting only the first would describe
// the wrong repository.
type BranchSource struct {
	Kind            string          `json:"kind"`
	Source          *SCMSourceInfo  `json:"source,omitempty"`
	Traits          []TraitInfo     `json:"traits,omitempty"`
	BuildStrategies []BuildStrategy `json:"buildStrategies,omitempty"`
}

// ScriptSource says which Jenkinsfile a job runs and where it is read from.
type ScriptSource struct {
	Origin        string `json:"origin"` // inline | scm | branch-source | unknown
	Class         string `json:"class"`
	ScriptPath    string `json:"scriptPath,omitempty"`
	Remote        string `json:"remote,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CredentialsID string `json:"credentialsId,omitempty"`
	Sandbox       *bool  `json:"sandbox,omitempty"`
	Lightweight   *bool  `json:"lightweight,omitempty"`
	ScriptLines   int    `json:"scriptLines,omitempty"`
}

// SCMSourceInfo is the repository a multibranch job indexes, the organization a
// folder navigates, or the remote a branch child was checked out from.
type SCMSourceInfo struct {
	Class         string `json:"class"`
	Provider      string `json:"provider"`
	ID            string `json:"id,omitempty"`
	APIURI        string `json:"apiUri,omitempty"`
	RepoOwner     string `json:"repoOwner,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Remote        string `json:"remote,omitempty"`
	CredentialsID string `json:"credentialsId,omitempty"`
}

// TraitInfo is one SCM source trait. Meaning is empty when the trait class is
// not one this tool knows how to decode; Class is always populated so an
// undecoded trait is still visible instead of silently dropped.
type TraitInfo struct {
	Class    string   `json:"class"`
	Name     string   `json:"name"`
	Meaning  string   `json:"meaning,omitempty"`
	Decoded  bool     `json:"decoded"`
	RawValue string   `json:"raw,omitempty"`
	Unread   []string `json:"unread,omitempty"`
}

// BuildStrategy is one entry of a source's buildStrategies. The combinators
// (all/any/none) nest, so children are kept, and a named-branch strategy is
// meaningless without its filters.
type BuildStrategy struct {
	Class    string          `json:"class"`
	Name     string          `json:"name"`
	Meaning  string          `json:"meaning,omitempty"`
	Decoded  bool            `json:"decoded"`
	Children []BuildStrategy `json:"children,omitempty"`
	Filters  []NameFilter    `json:"filters,omitempty"`
	Unread   []string        `json:"unread,omitempty"`
}

// NameFilter is one NamedBranchBuildStrategyImpl filter. The strategy builds a
// head only when a filter matches it, so the filter list is the whole answer to
// "why did this branch not build" and cannot be left out.
type NameFilter struct {
	Class   string   `json:"class"`
	Meaning string   `json:"meaning,omitempty"`
	Decoded bool     `json:"decoded"`
	Unread  []string `json:"unread,omitempty"`
}

// TriggerInfo is one configured trigger. Spec is the cron-ish schedule when the
// trigger has one.
type TriggerInfo struct {
	Class   string `json:"class"`
	Name    string `json:"name"`
	Spec    string `json:"spec,omitempty"`
	Meaning string `json:"meaning,omitempty"`
}

// RetentionPolicy is a build discarder. -1 means unlimited in Jenkins; the
// pointer being nil means the field was absent from config.xml.
type RetentionPolicy struct {
	Source             string `json:"source"`
	Class              string `json:"class,omitempty"`
	DaysToKeep         *int   `json:"daysToKeep,omitempty"`
	NumToKeep          *int   `json:"numToKeep,omitempty"`
	ArtifactDaysToKeep *int   `json:"artifactDaysToKeep,omitempty"`
	ArtifactNumToKeep  *int   `json:"artifactNumToKeep,omitempty"`
}

// downgradeXMLDecl rewrites a leading `<?xml version="1.1"?>` declaration to
// 1.0, the only version encoding/xml implements. The rewrite is confined to the
// declaration: end is the first "?>" in the document.
func downgradeXMLDecl(data []byte) []byte {
	end := bytes.Index(data, []byte("?>"))
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("<?xml")) || end < 0 {
		return data
	}
	head := bytes.ReplaceAll(data[:end], []byte(`version="1.1"`), []byte(`version="1.0"`))
	head = bytes.ReplaceAll(head, []byte(`version='1.1'`), []byte(`version='1.0'`))
	return append(head, data[end:]...)
}

var charRef = regexp.MustCompile(`&#[xX]?[0-9a-fA-F]+;`)

// stripUnrepresentable drops the characters that make Jenkins declare XML 1.1
// in the first place. C0 controls are legal in 1.1 but have no representation
// in 1.0, raw or as a character reference, and encoding/xml aborts the whole
// parse on one. They reach a config.xml through pasted terminal output, and
// dropping them costs nothing that anyone debugging a job needs.
func stripUnrepresentable(data []byte) []byte {
	data = charRef.ReplaceAllFunc(data, func(ref []byte) []byte {
		body, base := string(ref[2:len(ref)-1]), 10
		if body[0] == 'x' || body[0] == 'X' {
			body, base = body[1:], 16
		}
		n, err := strconv.ParseInt(body, base, 32)
		if err != nil || legalInXML10(rune(n)) {
			return ref
		}
		return nil
	})
	// Byte-wise so that multi-byte UTF-8 sequences pass through untouched:
	// every byte of one is >= 0x80.
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			continue
		}
		out = append(out, b)
	}
	return out
}

// legalInXML10 is the Char production of XML 1.0.
func legalInXML10(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// ParseJobConfig turns a job's config.xml into a JobDefinition.
func ParseJobConfig(data []byte) (*JobDefinition, error) {
	var root xmlNode
	if err := xml.Unmarshal(stripUnrepresentable(downgradeXMLDecl(data)), &root); err != nil {
		return nil, fmt.Errorf("parsing config.xml: %w", err)
	}

	rootName := root.XMLName.Local
	def := &JobDefinition{
		Class:       rootClass(rootName),
		Kind:        jobKindFromRoot(rootName),
		Container:   isContainerRoot(rootName),
		DisplayName: root.text("displayName"),
		Description: root.text("description"),
		Disabled:    root.boolText("disabled"),
	}

	def.Triggers = parseTriggers(root.child("triggers"))

	switch {
	case def.Container:
		parseContainer(&root, def)
	case rootName == "flow-definition":
		parsePipeline(&root, def)
	}

	if def.Retention == nil {
		def.Retention = parseLogRotator(root.child("logRotator"), "logRotator")
	}
	return def, nil
}

// SetJobPath records where the definition was read from, and for a branch child
// the parent multibranch job whose configuration holds the discovery rules.
func (d *JobDefinition) SetJobPath(path string) {
	d.JobPath = path
	if !d.branchChild {
		return
	}
	trimmed := strings.Trim(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i > 0 {
		d.Parent = trimmed[:i]
	}
}

// parseContainer reads a multibranch project or an organization folder: every
// branch source or navigator with its traits and build strategies, plus the
// factory that names the Jenkinsfile.
func parseContainer(root *xmlNode, def *JobDefinition) {
	def.Script = parseProjectFactory(root)

	for _, bs := range children(root.child("sources", "data")) {
		src := BranchSource{Kind: SourceBranch}
		if s := bs.child("source"); s != nil {
			src.Source = parseSCMSource(s)
			src.Traits = parseTraits(s.child("traits"))
		}
		src.BuildStrategies = parseBuildStrategies(bs.child("buildStrategies"))
		def.Sources = append(def.Sources, src)
		if def.Retention == nil {
			def.Retention = findRetention(bs.child("strategy"))
		}
	}

	// An organization folder pushes its own build strategies and branch
	// properties onto every child project it creates, so they belong to each
	// navigator rather than to the folder itself.
	orgStrategies := parseBuildStrategies(root.child("buildStrategies"))
	for _, nav := range children(root.child("navigators")) {
		def.Sources = append(def.Sources, BranchSource{
			Kind:            SourceNavigator,
			Source:          parseSCMSource(nav),
			Traits:          parseTraits(nav.child("traits")),
			BuildStrategies: orgStrategies,
		})
	}
	if def.Retention == nil {
		def.Retention = findRetention(root.child("strategy"))
	}
}

// parseProjectFactory finds the element that names the Jenkinsfile: <factory>
// on a multibranch project, <projectFactories> on an organization folder.
func parseProjectFactory(root *xmlNode) *ScriptSource {
	factory := root.child("factory")
	if factory == nil {
		if f := children(root.child("projectFactories")); len(f) > 0 {
			factory = f[0]
		}
	}
	if factory == nil {
		return nil
	}
	class := unescapeClass(factory.attr("class"))
	if class == "" {
		class = unescapeClass(factory.name())
	}
	return &ScriptSource{Origin: "branch-source", Class: class, ScriptPath: factory.text("scriptPath")}
}

// findRetention searches a branch-property strategy for a build discarder.
// XStream writes the property list either wrapped in an <a> array element or
// directly under <properties>, and NamedExceptionsBranchPropertyStrategy nests
// a further list per exception, so the whole subtree is walked.
func findRetention(n *xmlNode) *RetentionPolicy {
	if n == nil {
		return nil
	}
	if strings.HasSuffix(n.name(), "BuildRetentionBranchProperty") {
		if r := parseLogRotator(n.child("buildDiscarder"), "BuildRetentionBranchProperty"); r != nil {
			return r
		}
	}
	for i := range n.Children {
		if r := findRetention(&n.Children[i]); r != nil {
			return r
		}
	}
	return nil
}

// parsePipeline reads a flow-definition: a standalone pipeline job, or a branch
// child of a multibranch project.
func parsePipeline(root *xmlNode, def *JobDefinition) {
	if d := root.child("definition"); d != nil {
		def.Script = parseDefinition(d)
	}

	// A branch child records the source it was created from, including the git
	// remote and credentials actually used for the checkout.
	branch := root.child("properties", "org.jenkinsci.plugins.workflow.multibranch.BranchJobProperty", "branch")
	if branch == nil {
		return
	}
	def.Kind = "multibranch branch"
	def.branchChild = true
	if scm := branch.child("scm"); scm != nil {
		info := parseSCMSource(scm)
		info.ID = branch.text("sourceId")
		def.Sources = append(def.Sources, BranchSource{Kind: SourceCheckout, Source: info})
	}
	if def.Script != nil && def.Script.Branch == "" {
		def.Script.Branch = branch.text("head", "name")
	}
}

func parseDefinition(d *xmlNode) *ScriptSource {
	class := unescapeClass(d.attr("class"))
	s := &ScriptSource{Class: class, Origin: "unknown", ScriptPath: d.text("scriptPath")}

	switch shortClass(class) {
	case "CpsFlowDefinition":
		s.Origin = "inline"
		s.Sandbox = d.boolText("sandbox")
		if script := strings.TrimSpace(d.text("script")); script != "" {
			s.ScriptLines = strings.Count(script, "\n") + 1
		}
	case "CpsScmFlowDefinition":
		s.Origin = "scm"
		s.Lightweight = d.boolText("lightweight")
		if scm := d.child("scm"); scm != nil {
			git := parseSCMSource(scm)
			s.Remote, s.CredentialsID = git.Remote, git.CredentialsID
			s.Branch = scm.text("branches", "hudson.plugins.git.BranchSpec", "name")
		}
	case "SCMBinder":
		// The multibranch parent decides the repository; this job only records
		// which file inside it is the pipeline.
		s.Origin = "branch-source"
	}
	return s
}

// parseSCMSource handles a multibranch <source>, an organization <navigator>
// and a branch child's GitSCM <scm> element. A navigator carries its class as
// the element name rather than a class attribute.
func parseSCMSource(src *xmlNode) *SCMSourceInfo {
	class := unescapeClass(src.attr("class"))
	if class == "" && strings.Contains(src.name(), ".") {
		class = unescapeClass(src.name())
	}
	info := &SCMSourceInfo{
		Class:         class,
		Provider:      scmProvider(class),
		ID:            src.text("id"),
		APIURI:        src.text("apiUri"),
		RepoOwner:     src.text("repoOwner"),
		Repository:    src.text("repository"),
		Remote:        src.text("remote"),
		CredentialsID: src.text("credentialsId"),
	}
	if info.Remote == "" {
		info.Remote = src.text("userRemoteConfigs", "hudson.plugins.git.UserRemoteConfig", "url")
	}
	if info.CredentialsID == "" {
		info.CredentialsID = src.text("userRemoteConfigs", "hudson.plugins.git.UserRemoteConfig", "credentialsId")
	}
	return info
}

// scmProvider names the forge a source or navigator talks to. Anything else is
// "unknown", which makes the caller fall back to printing the class.
func scmProvider(class string) string {
	switch {
	case strings.Contains(class, "GitHub"):
		return "GitHub"
	case strings.Contains(class, "Bitbucket"):
		return "Bitbucket"
	case strings.Contains(class, "GitLab"):
		return "GitLab"
	case strings.Contains(class, "GitSCM"):
		return "Git"
	default:
		return "unknown"
	}
}

func parseTriggers(triggers *xmlNode) []TriggerInfo {
	var out []TriggerInfo
	for _, t := range children(triggers) {
		class := unescapeClass(t.name())
		info := TriggerInfo{Class: class, Name: humanize(shortClass(class)), Spec: t.text("spec")}
		if shortClass(class) == "PeriodicFolderTrigger" {
			info.Meaning = "re-index the repository on this schedule; branches added between scans do not build until the next scan"
			if ms, ok := atoi(t.text("interval")); ok {
				info.Meaning = fmt.Sprintf("re-index the repository about every %s", humanMillis(ms)) +
					"; branches added between scans do not build until the next scan"
			}
		}
		out = append(out, info)
	}
	return out
}

func parseLogRotator(n *xmlNode, source string) *RetentionPolicy {
	if n == nil {
		return nil
	}
	return &RetentionPolicy{
		Source:             source,
		Class:              unescapeClass(n.attr("class")),
		DaysToKeep:         n.intText("daysToKeep"),
		NumToKeep:          n.intText("numToKeep"),
		ArtifactDaysToKeep: n.intText("artifactDaysToKeep"),
		ArtifactNumToKeep:  n.intText("artifactNumToKeep"),
	}
}

// humanMillis renders a duration the way the Jenkins UI phrases it. The
// remainder is kept, so 25h does not read as "1d".
func humanMillis(ms int) string {
	units := []struct {
		size int
		suf  string
	}{{86400000, "d"}, {3600000, "h"}, {60000, "m"}, {1000, "s"}}
	var parts []string
	for _, u := range units {
		if len(parts) == 2 {
			break
		}
		if n := ms / u.size; n > 0 {
			parts = append(parts, strconv.Itoa(n)+u.suf)
			ms -= n * u.size
		}
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

func rootClass(rootName string) string {
	switch rootName {
	case "flow-definition":
		return "org.jenkinsci.plugins.workflow.job.WorkflowJob"
	case "project":
		return "hudson.model.FreeStyleProject"
	default:
		return unescapeClass(rootName)
	}
}

// isContainerRoot says whether the root element is a job that holds other jobs.
// Only these have a pipeline definition to look for inside them; every other
// root does its own building, whether or not this tool can read how.
func isContainerRoot(rootName string) bool {
	return strings.Contains(rootName, "MultiBranch") ||
		strings.Contains(rootName, "OrganizationFolder") ||
		strings.Contains(rootName, "Folder")
}

// jobKindFromRoot names the job type. A root element outside the known set is
// reported as unrecognized rather than turned into a plausible-looking type:
// Class carries what Jenkins actually wrote.
func jobKindFromRoot(rootName string) string {
	switch {
	case strings.Contains(rootName, "OrganizationFolder"):
		return "organization folder"
	case strings.Contains(rootName, "MultiBranch"):
		return "multibranch pipeline"
	case strings.Contains(rootName, "Folder"):
		return "folder"
	case rootName == "flow-definition":
		return "pipeline"
	case rootName == "project":
		return "freestyle"
	default:
		return "unrecognized"
	}
}

// unescapeClass reverses XStream's element-name escaping: a class name becomes
// an XML element with "_" written "__" and "$" written "_-".
func unescapeClass(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	s = strings.ReplaceAll(s, "_-", "$")
	return strings.ReplaceAll(s, "__", "_")
}

func shortClass(class string) string {
	if i := strings.LastIndexAny(class, ".$"); i >= 0 {
		return class[i+1:]
	}
	return class
}

// humanize turns "BranchDiscoveryTrait" into "Branch discovery". Acronym runs
// stay whole, so "SCMBinder" reads as "SCM binder" and not "Scmbinder".
func humanize(short string) string {
	short = strings.TrimSuffix(short, "Trait")
	short = strings.TrimSuffix(short, "Impl")
	if short == "" {
		return short
	}
	var words []string
	start := 0
	for i := 1; i < len(short); i++ {
		lowerToUpper := isUpper(short[i]) && !isUpper(short[i-1])
		// The last capital of a run starts the next word: the "B" of "SCMBinder".
		endOfRun := isUpper(short[i-1]) && isUpper(short[i]) && i+1 < len(short) && !isUpper(short[i+1])
		if lowerToUpper || endOfRun {
			words = append(words, short[start:i])
			start = i
		}
	}
	words = append(words, short[start:])
	for i, w := range words {
		switch {
		case isAcronym(w):
		case i == 0:
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		default:
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

func isAcronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for i := 0; i < len(w); i++ {
		if !isUpper(w[i]) {
			return false
		}
	}
	return true
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}

// xmlNode is a generic config.xml element. config.xml encodes plugin classes as
// element names, so the shape cannot be described with struct tags: unknown
// elements must survive parsing to be reported rather than dropped.
type xmlNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []xmlNode  `xml:",any"`
	Data     string     `xml:",chardata"`
}

func (n *xmlNode) name() string { return n.XMLName.Local }

func (n *xmlNode) attr(name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// child walks a chain of element names, returning nil if any step is missing.
func (n *xmlNode) child(path ...string) *xmlNode {
	cur := n
	for _, want := range path {
		if cur == nil {
			return nil
		}
		var next *xmlNode
		for i := range cur.Children {
			if cur.Children[i].name() == want {
				next = &cur.Children[i]
				break
			}
		}
		cur = next
	}
	return cur
}

// children returns every child element of n.
func children(n *xmlNode) []*xmlNode {
	if n == nil {
		return nil
	}
	out := make([]*xmlNode, 0, len(n.Children))
	for i := range n.Children {
		out = append(out, &n.Children[i])
	}
	return out
}

// unreadChildren lists the child elements of n that the decoder for its class
// does not look at. A configured element nobody reads would otherwise leave a
// decoded line looking complete when it is not.
func unreadChildren(n *xmlNode, read ...string) []string {
	var out []string
	for _, c := range children(n) {
		name := c.name()
		known := false
		for _, r := range read {
			if r == name {
				known = true
				break
			}
		}
		if !known {
			out = append(out, name)
		}
	}
	return out
}

func (n *xmlNode) text(path ...string) string {
	c := n.child(path...)
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Data)
}

func (n *xmlNode) intText(path ...string) *int {
	v, ok := atoi(n.text(path...))
	if !ok {
		return nil
	}
	return &v
}

func (n *xmlNode) boolText(path ...string) *bool {
	switch n.text(path...) {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	default:
		return nil
	}
}

func isUpper(b byte) bool { return b >= 'A' && b <= 'Z' }
