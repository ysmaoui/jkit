package jenkins

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadConfig(t *testing.T, name string) *JobDefinition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	def, err := ParseJobConfig(data)
	require.NoError(t, err)
	return def
}

func onlySource(t *testing.T, def *JobDefinition) BranchSource {
	t.Helper()
	require.Len(t, def.Sources, 1)
	return def.Sources[0]
}

func traitByClass(bs BranchSource, suffix string) *TraitInfo {
	for i := range bs.Traits {
		if strings.HasSuffix(bs.Traits[i].Class, suffix) {
			return &bs.Traits[i]
		}
	}
	return nil
}

func strategyByClass(list []BuildStrategy, suffix string) *BuildStrategy {
	for i := range list {
		if strings.HasSuffix(list[i].Class, suffix) {
			return &list[i]
		}
	}
	return nil
}

// mustParseNode parses a single element so a decoder can be exercised without a
// whole config.xml around it.
func mustParseNode(t *testing.T, doc string) *xmlNode {
	t.Helper()
	var n xmlNode
	require.NoError(t, xml.Unmarshal([]byte(doc), &n))
	return &n
}

func TestParseMultibranchConfig(t *testing.T) {
	def := loadConfig(t, "config_multibranch.xml")

	assert.Equal(t, "multibranch pipeline", def.Kind)
	assert.Equal(t, "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject", def.Class)
	assert.True(t, def.Container)
	require.NotNil(t, def.Disabled)
	assert.False(t, *def.Disabled)

	// Which Jenkinsfile runs.
	require.NotNil(t, def.Script)
	assert.Equal(t, "branch-source", def.Script.Origin)
	assert.Equal(t, "Jenkinsfile", def.Script.ScriptPath)

	// Where it comes from, and with which credentials.
	src := onlySource(t, def)
	assert.Equal(t, SourceBranch, src.Kind)
	require.NotNil(t, src.Source)
	assert.Equal(t, "GitHub", src.Source.Provider)
	assert.Equal(t, "https://github.example.com/api/v3", src.Source.APIURI)
	assert.Equal(t, "ACME", src.Source.RepoOwner)
	assert.Equal(t, "widget", src.Source.Repository)
	assert.Equal(t, "acme-github-app", src.Source.CredentialsID)

	// Retention comes from the branch property, not a logRotator.
	require.NotNil(t, def.Retention)
	assert.Equal(t, "BuildRetentionBranchProperty", def.Retention.Source)
	require.NotNil(t, def.Retention.DaysToKeep)
	assert.Equal(t, 30, *def.Retention.DaysToKeep)
	require.NotNil(t, def.Retention.NumToKeep)
	assert.Equal(t, 20, *def.Retention.NumToKeep)

	// Re-indexing schedule.
	require.Len(t, def.Triggers, 1)
	assert.Equal(t, "H H/4 * * *", def.Triggers[0].Spec)
	assert.Contains(t, def.Triggers[0].Class, "PeriodicFolderTrigger")
}

// Every numeric strategy id must reach the reader as words.
func TestMultibranchTraitsAreDecoded(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_multibranch.xml"))

	branch := traitByClass(src, "BranchDiscoveryTrait")
	require.NotNil(t, branch)
	assert.True(t, branch.Decoded)
	assert.Equal(t, "3", branch.RawValue)
	assert.Equal(t, "all branches", branch.Meaning)

	pr := traitByClass(src, "OriginPullRequestDiscoveryTrait")
	require.NotNil(t, pr)
	assert.True(t, pr.Decoded)
	assert.Equal(t, "1", pr.RawValue)
	assert.Contains(t, pr.Meaning, "merged with the current target branch")

	tag := traitByClass(src, "TagDiscoveryTrait")
	require.NotNil(t, tag)
	assert.True(t, tag.Decoded)

	wildcard := traitByClass(src, "WildcardSCMHeadFilterTrait")
	require.NotNil(t, wildcard)
	assert.True(t, wildcard.Decoded)
	assert.Contains(t, wildcard.Meaning, `"*"`)
}

// Fork PR discovery decides whether a PR from a fork is built at all, and the
// trust class decides whose code is allowed to run, so both must be reported.
func TestForkPullRequestDiscoveryIsDecoded(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_multibranch.xml"))

	fork := traitByClass(src, "ForkPullRequestDiscoveryTrait")
	require.NotNil(t, fork)
	assert.True(t, fork.Decoded)
	assert.Equal(t, "2", fork.RawValue)
	assert.Contains(t, fork.Meaning, "the current PR head revision")
	assert.Contains(t, fork.Meaning, "trust TrustPermission")
}

func TestRegexHeadFilterTraitIsDecoded(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_named_branches.xml"))

	regex := traitByClass(src, "RegexSCMHeadFilterTrait")
	require.NotNil(t, regex)
	assert.True(t, regex.Decoded)
	assert.Equal(t, "(main|release/.*)", regex.RawValue)
	assert.Contains(t, regex.Meaning, `only heads matching regex "(main|release/.*)"`)
}

// A trait class the parser does not know must still be reported. Dropping it
// would make the discovery summary claim rules it never read.
func TestUnknownTraitIsReported(t *testing.T) {
	doc := []byte(`<?xml version='1.1' encoding='UTF-8'?>
<org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject>
  <sources><data><jenkins.branch.BranchSource>
    <source class="org.jenkinsci.plugins.github_branch_source.GitHubSCMSource">
      <traits>
        <com.example.totally__made__up.MysteryFilterTrait>
          <threshold>4</threshold>
        </com.example.totally__made__up.MysteryFilterTrait>
      </traits>
    </source>
  </jenkins.branch.BranchSource></data></sources>
</org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject>`)

	def, err := ParseJobConfig(doc)
	require.NoError(t, err)
	src := onlySource(t, def)
	require.Len(t, src.Traits, 1)
	assert.False(t, src.Traits[0].Decoded)
	// Element-name escaping is reversed so the class matches the real one.
	assert.Equal(t, "com.example.totally_made_up.MysteryFilterTrait", src.Traits[0].Class)
}

// A trait the parser decodes but does not read whole is worse than an unknown
// one: the line looks complete. The unread elements must be named.
func TestConfiguredButUnreadElementsAreFlagged(t *testing.T) {
	node := mustParseNode(t, `<org.jenkinsci.plugins.github__branch__source.BranchDiscoveryTrait>
	  <strategyId>3</strategyId>
	  <someFutureOption>true</someFutureOption>
	</org.jenkinsci.plugins.github__branch__source.BranchDiscoveryTrait>`)
	tr := decodeTrait(node)
	assert.True(t, tr.Decoded)
	assert.Equal(t, []string{"someFutureOption"}, tr.Unread)
}

// The real config carries a trait this tool does not decode; it must survive.
func TestUndecodedTraitInRealConfigSurvives(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_multibranch.xml"))
	clone := traitByClass(src, "CloneOptionTrait")
	require.NotNil(t, clone, "CloneOptionTrait was dropped from the trait list")
	assert.False(t, clone.Decoded)
	assert.Equal(t, "jenkins.plugins.git.traits.CloneOptionTrait", clone.Class)
}

// An id outside the documented set is not guessed at: the raw value is kept and
// the trait is marked undecoded.
func TestUnknownStrategyIDIsNotGuessed(t *testing.T) {
	node := mustParseNode(t, `<org.jenkinsci.plugins.github__branch__source.BranchDiscoveryTrait><strategyId>9</strategyId></org.jenkinsci.plugins.github__branch__source.BranchDiscoveryTrait>`)
	tr := decodeTrait(node)
	assert.False(t, tr.Decoded)
	assert.Equal(t, "9", tr.RawValue)
	assert.Empty(t, tr.Meaning)
	assert.Equal(t, "org.jenkinsci.plugins.github_branch_source.BranchDiscoveryTrait", tr.Class)
}

func TestBuildStrategiesAreDecodedAndNested(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_multibranch.xml"))
	require.Len(t, src.BuildStrategies, 1)

	all := src.BuildStrategies[0]
	assert.True(t, all.Decoded)
	assert.Contains(t, all.Class, "AllBranchBuildStrategyImpl")
	require.Len(t, all.Children, 2)

	skip := all.Children[0]
	assert.True(t, skip.Decoded)
	assert.Contains(t, skip.Class, "SkipInitialBuildOnFirstBranchIndexing")
	assert.Contains(t, skip.Meaning, "first discovered")

	any := all.Children[1]
	require.Len(t, any.Children, 2)
	assert.Contains(t, any.Children[1].Class, "TagBuildStrategyImpl")
	// atMostMillis=23328000000 is 270 days; atLeastMillis=-1 is no lower bound.
	assert.Contains(t, any.Children[1].Meaning, "at most 270d old")
	assert.NotContains(t, any.Children[1].Meaning, "at least")
}

// All three combinators return false on an empty list, so an empty one blocks
// every build. Reporting only the label reads as the opposite.
func TestEmptyCombinatorBlocksEveryBuild(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_named_branches.xml"))
	all := strategyByClass(src.BuildStrategies, "AllBranchBuildStrategyImpl")
	require.NotNil(t, all)
	assert.Empty(t, all.Children)
	assert.Equal(t, "no nested strategy is configured, so this strategy never starts a build", all.Meaning)

	for _, class := range []string{"AnyBranchBuildStrategyImpl", "NoneBranchBuildStrategyImpl"} {
		s := decodeBuildStrategy(mustParseNode(t, "<jenkins.branch.buildstrategies.basic."+class+"><strategies/></jenkins.branch.buildstrategies.basic."+class+">"))
		assert.Contains(t, s.Meaning, "never starts a build", class)
	}
}

// The name filters are the whole answer to "why did feature/x not build", so a
// named-branch strategy without them is a complete-looking lie.
func TestNamedBranchFiltersAreReported(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_named_branches.xml"))
	named := strategyByClass(src.BuildStrategies, "NamedBranchBuildStrategyImpl")
	require.NotNil(t, named)
	require.Len(t, named.Filters, 2)

	assert.True(t, named.Filters[0].Decoded)
	assert.Equal(t, "jenkins.branch.buildstrategies.basic.NamedBranchBuildStrategyImpl$RegexNameFilter", named.Filters[0].Class)
	assert.Equal(t, `name matches regex "release/.*"`, named.Filters[0].Meaning)

	assert.True(t, named.Filters[1].Decoded)
	assert.Equal(t, `name is exactly "main", ignoring case`, named.Filters[1].Meaning)
}

// An empty filter list makes isAutomaticBuild return false for every head.
func TestNamedBranchWithoutFiltersBlocksEveryBuild(t *testing.T) {
	s := decodeBuildStrategy(mustParseNode(t,
		`<jenkins.branch.buildstrategies.basic.NamedBranchBuildStrategyImpl><filters/></jenkins.branch.buildstrategies.basic.NamedBranchBuildStrategyImpl>`))
	assert.True(t, s.Decoded)
	assert.Empty(t, s.Filters)
	assert.Contains(t, s.Meaning, "never starts a build")
}

// ignoreTargetOnlyChanges stops a PR rebuilding when only its target branch
// moved, which is a common reason a PR looks stuck.
func TestChangeRequestFlagsAreReported(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_named_branches.xml"))
	cr := strategyByClass(src.BuildStrategies, "ChangeRequestBuildStrategyImpl")
	require.NotNil(t, cr)
	assert.Contains(t, cr.Meaning, "not when the only change is the target branch moving")
	assert.NotContains(t, cr.Meaning, "untrusted")

	both := decodeBuildStrategy(mustParseNode(t, `<jenkins.branch.buildstrategies.basic.ChangeRequestBuildStrategyImpl>
	  <ignoreTargetOnlyChanges>true</ignoreTargetOnlyChanges>
	  <ignoreUntrustedChanges>true</ignoreUntrustedChanges>
	</jenkins.branch.buildstrategies.basic.ChangeRequestBuildStrategyImpl>`))
	assert.Contains(t, both.Meaning, "untrusted author")
}

// A minimum tag age above the maximum makes isAutomaticBuild return false for
// every tag. Rendering the two bounds as an ordinary window hides that.
func TestImpossibleTagWindowSaysNoTagBuilds(t *testing.T) {
	src := onlySource(t, loadConfig(t, "config_named_branches.xml"))
	tag := strategyByClass(src.BuildStrategies, "TagBuildStrategyImpl")
	require.NotNil(t, tag)
	assert.Contains(t, tag.Meaning, "no tag ever builds")
	assert.Contains(t, tag.Meaning, "300d")
	assert.Contains(t, tag.Meaning, "7d")
}

func TestUnknownBuildStrategyIsReported(t *testing.T) {
	node := mustParseNode(t, `<com.example.WeirdBuildStrategy/>`)
	s := decodeBuildStrategy(node)
	assert.False(t, s.Decoded)
	assert.Equal(t, "com.example.WeirdBuildStrategy", s.Class)
}

// Reading only the first branch source would describe the wrong repository for
// every branch that comes from the second one.
func TestEveryBranchSourceIsParsed(t *testing.T) {
	def := loadConfig(t, "config_two_sources.xml")
	require.Len(t, def.Sources, 2)

	assert.Equal(t, "GitHub", def.Sources[0].Source.Provider)
	assert.Equal(t, "widget", def.Sources[0].Source.Repository)
	assert.Len(t, def.Sources[0].Traits, 1)

	assert.Equal(t, "Bitbucket", def.Sources[1].Source.Provider)
	assert.Equal(t, "gadget", def.Sources[1].Source.Repository)
	assert.Equal(t, "acme-bitbucket", def.Sources[1].Source.CredentialsID)
	assert.Empty(t, def.Sources[1].Traits, "an empty traits element discovers nothing, and must not borrow the first source's")
	assert.Empty(t, def.Sources[1].BuildStrategies)
}

// An organization folder's navigator traits decide what every child job
// discovers, and its build strategies are pushed onto each child.
func TestParseOrganizationFolder(t *testing.T) {
	def := loadConfig(t, "config_org_folder.xml")

	assert.Equal(t, "organization folder", def.Kind)
	assert.True(t, def.Container)

	require.NotNil(t, def.Script, "the project factory names the Jenkinsfile every child runs")
	assert.Equal(t, "ci/Jenkinsfile", def.Script.ScriptPath)

	nav := onlySource(t, def)
	assert.Equal(t, SourceNavigator, nav.Kind)
	require.NotNil(t, nav.Source)
	assert.Equal(t, "GitHub", nav.Source.Provider)
	assert.Equal(t, "org.jenkinsci.plugins.github_branch_source.GitHubSCMNavigator", nav.Source.Class)
	assert.Equal(t, "ACME", nav.Source.RepoOwner)

	branch := traitByClass(nav, "BranchDiscoveryTrait")
	require.NotNil(t, branch)
	assert.Equal(t, "branches that are not also filed as PRs", branch.Meaning)

	repoFilter := traitByClass(nav, "RegexSCMSourceFilterTrait")
	require.NotNil(t, repoFilter, "the repository filter decides which repos become jobs at all")
	assert.True(t, repoFilter.Decoded)
	assert.Contains(t, repoFilter.Meaning, `"widget-.*"`)

	require.Len(t, nav.BuildStrategies, 1)
	assert.Contains(t, nav.BuildStrategies[0].Class, "SkipInitialBuildOnFirstBranchIndexing")
}

// XStream writes the branch property list either wrapped in an <a> array
// element or directly, so the retention must be found under both.
func TestRetentionFoundWithoutArrayWrapper(t *testing.T) {
	def := loadConfig(t, "config_org_folder.xml")
	require.NotNil(t, def.Retention)
	assert.Equal(t, "BuildRetentionBranchProperty", def.Retention.Source)
	require.NotNil(t, def.Retention.NumToKeep)
	assert.Equal(t, 10, *def.Retention.NumToKeep)
}

// The trigger interval must not be truncated to its largest unit: 25h is not
// the same re-index schedule as a day.
func TestPeriodicTriggerIntervalKeepsTheRemainder(t *testing.T) {
	def := loadConfig(t, "config_org_folder.xml")
	require.Len(t, def.Triggers, 1)
	assert.Contains(t, def.Triggers[0].Meaning, "every 1d 1h")
}

func TestParseBranchChildConfig(t *testing.T) {
	def := loadConfig(t, "config_branch.xml")

	assert.Equal(t, "multibranch branch", def.Kind)
	assert.False(t, def.Container)
	assert.Equal(t, "feature/build-tweak", def.DisplayName)

	require.NotNil(t, def.Script)
	assert.Equal(t, "branch-source", def.Script.Origin)
	assert.Equal(t, "Jenkinsfile", def.Script.ScriptPath)
	assert.Equal(t, "feature/build-tweak", def.Script.Branch)

	src := onlySource(t, def)
	assert.Equal(t, SourceCheckout, src.Kind)
	require.NotNil(t, src.Source)
	assert.Equal(t, "https://github.example.com/ACME/widget.git", src.Source.Remote)
	assert.Equal(t, "acme-github-app", src.Source.CredentialsID)
	assert.Equal(t, "ACME/widget", src.Source.ID)

	require.NotNil(t, def.Retention)
	assert.Equal(t, "logRotator", def.Retention.Source)
	require.NotNil(t, def.Retention.DaysToKeep)
	assert.Equal(t, 30, *def.Retention.DaysToKeep)
	require.NotNil(t, def.Retention.ArtifactNumToKeep)
	assert.Equal(t, -1, *def.Retention.ArtifactNumToKeep)
}

// Parent is where the discovery rules actually live. It must be derived from
// the parsed document, not from a display string a reword could change.
func TestSetJobPathDerivesTheParentOfABranchChild(t *testing.T) {
	def := loadConfig(t, "config_branch.xml")
	def.Kind = "reworded by a later change"
	def.SetJobPath("team/service/feature%2Fbuild-tweak")
	assert.Equal(t, "team/service/feature%2Fbuild-tweak", def.JobPath)
	assert.Equal(t, "team/service", def.Parent)

	container := loadConfig(t, "config_multibranch.xml")
	container.SetJobPath("team/service")
	assert.Empty(t, container.Parent, "a multibranch container is not a branch child")
}

func TestParseInlinePipelineConfig(t *testing.T) {
	def := loadConfig(t, "config_inline_pipeline.xml")

	assert.Equal(t, "pipeline", def.Kind)
	require.NotNil(t, def.Disabled)
	assert.True(t, *def.Disabled, "job is disabled and must be reported as such")

	require.NotNil(t, def.Script)
	assert.Equal(t, "inline", def.Script.Origin)
	assert.Equal(t, 6, def.Script.ScriptLines)
	require.NotNil(t, def.Script.Sandbox)
	assert.True(t, *def.Script.Sandbox)
	assert.Empty(t, def.Sources, "an inline script has no repository")
}

func TestParseScmPipelineConfig(t *testing.T) {
	def := loadConfig(t, "config_scm_pipeline.xml")

	require.NotNil(t, def.Script)
	assert.Equal(t, "scm", def.Script.Origin)
	assert.Equal(t, "ci/Jenkinsfile", def.Script.ScriptPath)
	assert.Equal(t, "https://github.example.com/ACME/widget.git", def.Script.Remote)
	assert.Equal(t, "*/main", def.Script.Branch)
	assert.Equal(t, "acme-github-app", def.Script.CredentialsID)

	require.NotNil(t, def.Retention)
	require.NotNil(t, def.Retention.DaysToKeep)
	assert.Equal(t, 7, *def.Retention.DaysToKeep)
	require.NotNil(t, def.Retention.NumToKeep)
	assert.Equal(t, -1, *def.Retention.NumToKeep)
}

// A definition class the parser does not know keeps its class name so the
// reader can look it up, instead of being reported as a known origin.
func TestUnknownDefinitionClassIsReported(t *testing.T) {
	doc := []byte(`<flow-definition>
  <definition class="com.example.SomeOtherFlowDefinition"><scriptPath>Jenkinsfile</scriptPath></definition>
</flow-definition>`)
	def, err := ParseJobConfig(doc)
	require.NoError(t, err)
	require.NotNil(t, def.Script)
	assert.Equal(t, "unknown", def.Script.Origin)
	assert.Equal(t, "com.example.SomeOtherFlowDefinition", def.Script.Class)
}

func TestParseFolderConfig(t *testing.T) {
	def := loadConfig(t, "config_folder.xml")
	assert.Equal(t, "folder", def.Kind)
	assert.True(t, def.Container)
	assert.Equal(t, "Sandbox", def.DisplayName)
	assert.Nil(t, def.Script)
	assert.Empty(t, def.Sources)
}

// A freestyle job builds by itself. Treating it as a container would tell the
// reader to go looking for a job inside it that does not exist.
func TestFreestyleIsNotAContainer(t *testing.T) {
	def := loadConfig(t, "config_freestyle.xml")
	assert.Equal(t, "freestyle", def.Kind)
	assert.False(t, def.Container)
	assert.Equal(t, "hudson.model.FreeStyleProject", def.Class)
}

// An unexpected root element is reported as unrecognized rather than turned
// into a plausible-looking job type; Class keeps what Jenkins wrote.
func TestUnrecognizedRootIsNotGuessed(t *testing.T) {
	def, err := ParseJobConfig([]byte(`<hudson><description>not a job at all</description></hudson>`))
	require.NoError(t, err)
	assert.Equal(t, "unrecognized", def.Kind)
	assert.Equal(t, "hudson", def.Class)
	assert.False(t, def.Container)
}

// Jenkins writes config.xml as XML 1.1, which encoding/xml rejects outright.
func TestParsesXML11Declaration(t *testing.T) {
	def, err := ParseJobConfig([]byte(`<?xml version='1.1' encoding='UTF-8'?><flow-definition><disabled>true</disabled></flow-definition>`))
	require.NoError(t, err)
	require.NotNil(t, def.Disabled)
	assert.True(t, *def.Disabled)
}

// A C0 control character is the reason Jenkins declares XML 1.1 at all, and it
// used to abort the whole parse. Losing the character beats losing the answer.
func TestControlCharactersDoNotKillTheParse(t *testing.T) {
	def := loadConfig(t, "config_control_chars.xml")
	assert.Contains(t, def.Description, "[31mFAILED")
	assert.NotContains(t, def.Description, "\x1b")
	require.NotNil(t, def.Script)
	assert.Equal(t, "inline", def.Script.Origin)
}

// The declaration rewrite must not reach into the body, and a legal character
// reference must survive.
func TestSanitizeLeavesLegalContentAlone(t *testing.T) {
	def, err := ParseJobConfig([]byte(
		`<?xml version='1.1' encoding='UTF-8'?><flow-definition><description>tab&#x9;kept, version='1.1' kept, &#65; kept</description></flow-definition>`))
	require.NoError(t, err)
	assert.Equal(t, "tab\tkept, version='1.1' kept, A kept", def.Description)
}

func TestUnescapeClass(t *testing.T) {
	assert.Equal(t, "org.jenkinsci.plugins.github_branch_source.BranchDiscoveryTrait",
		unescapeClass("org.jenkinsci.plugins.github__branch__source.BranchDiscoveryTrait"))
	assert.Equal(t, "jenkins.branch.MultiBranchProject$BranchSourceList",
		unescapeClass("jenkins.branch.MultiBranchProject_-BranchSourceList"))
	assert.Equal(t, "flow-definition", unescapeClass("flow-definition"))
}

func TestHumanize(t *testing.T) {
	assert.Equal(t, "Clone option", humanize("CloneOptionTrait"))
	assert.Equal(t, "Periodic folder trigger", humanize("PeriodicFolderTrigger"))
	// An acronym run must stay whole: this is the name an undecoded definition
	// class is rendered under, which is where readability matters most.
	assert.Equal(t, "SCM binder", humanize("SCMBinder"))
	assert.Equal(t, "Wildcard SCM head filter", humanize("WildcardSCMHeadFilterTrait"))
}

func TestHumanMillis(t *testing.T) {
	assert.Equal(t, "12h", humanMillis(43200000))
	assert.Equal(t, "270d", humanMillis(23328000000))
	assert.Equal(t, "1d 1h", humanMillis(90000000), "truncating to 1d would misreport the schedule")
	assert.Equal(t, "0s", humanMillis(0))
}
