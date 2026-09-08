package jenkins

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lib(name, version string) SharedLibrary {
	return SharedLibrary{Name: name, Version: version}
}

func checkout(remote, sha string, branches ...string) GitCheckout {
	return GitCheckout{ActionClass: "hudson.plugins.git.util.BuildData",
		RemoteURLs: []string{remote}, SHA1: sha, Branches: branches}
}

func TestResolveLibrarySHAsMatchesUniqueBranch(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}
	checkouts := []GitCheckout{checkout("https://git/hera-2.git", "52d790ee", "develop")}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryMatchedBranch, libs[0].Resolution)
	assert.Equal(t, "52d790ee", libs[0].SHA1)
	assert.Equal(t, "https://git/hera-2.git", libs[0].RemoteURL)
	assert.Equal(t, "hera2", checkouts[0].Library, "the checkout should name the library it was attributed to")
}

// Two libraries on one branch name make every checkout ambiguous between them.
// Attributing the checkout to either would be a coin flip printed as a fact.
func TestResolveLibrarySHAsRefusesSharedBranchName(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop"), lib("premium", "develop")}
	checkouts := []GitCheckout{
		checkout("https://git/hera-2.git", "52d790ee", "develop"),
		checkout("https://git/premium.git", "37cecaf2", "develop"),
	}

	ResolveLibrarySHAs(libs, checkouts)

	for _, l := range libs {
		assert.Equal(t, LibraryUnresolved, l.Resolution)
		assert.Empty(t, l.SHA1)
		assert.Contains(t, l.Reason, "2 libraries request \"develop\"")
	}
	assert.Empty(t, checkouts[0].Library)
	assert.Empty(t, checkouts[1].Library)
}

// The library's own repository plus a pipeline checkout of a different repo on
// the same branch name. Nothing distinguishes them, so neither is claimed.
func TestResolveLibrarySHAsRefusesTwoCheckoutsOnOneBranch(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}
	checkouts := []GitCheckout{
		checkout("https://git/hera-2.git", "52d790ee", "develop"),
		checkout("https://git/the-app.git", "aaaabbbb", "develop"),
	}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryUnresolved, libs[0].Resolution)
	assert.Empty(t, libs[0].SHA1)
	assert.Contains(t, libs[0].Reason, "2 different checkouts recorded the branch \"develop\"")
}

// Two BuildData for one repository at one commit is the git plugin recording
// the same checkout twice. The answer does not depend on which one is read.
func TestResolveLibrarySHAsAcceptsDuplicateIdenticalCheckouts(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}
	checkouts := []GitCheckout{
		checkout("https://git/hera-2.git", "52d790ee", "develop"),
		checkout("https://git/hera-2.git", "52d790ee", "develop"),
	}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryMatchedBranch, libs[0].Resolution)
	assert.Equal(t, "52d790ee", libs[0].SHA1)
	assert.Equal(t, "hera2", checkouts[1].Library)
}

// A version that is already a commit id is not a join at all.
func TestResolveLibrarySHAsPinsCommitVersion(t *testing.T) {
	sha := "e3fc9e5c9c9ab32e6318ea088d67c6c2b29b7aa0"
	libs := []SharedLibrary{lib("hera2", sha)}

	ResolveLibrarySHAs(libs, nil)

	assert.Equal(t, LibraryPinned, libs[0].Resolution)
	assert.Equal(t, sha, libs[0].SHA1)
	assert.False(t, libs[0].Moving())
}

func TestResolveLibrarySHAsRejectsAbbreviatedHashAsCommit(t *testing.T) {
	// Seven hex characters is also a legal branch name, so it is not evidence.
	libs := []SharedLibrary{lib("hera2", "e3fc9e5")}

	ResolveLibrarySHAs(libs, []GitCheckout{checkout("https://git/hera-2.git", "52d790ee", "develop")})

	assert.Equal(t, LibraryUnresolved, libs[0].Resolution)
	assert.Empty(t, libs[0].SHA1)
}

// A tag never reaches the branch list, so it stays unresolved rather than
// borrowing the SHA of whatever else the build checked out.
func TestResolveLibrarySHAsLeavesTagUnresolved(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "v1.4.2")}
	checkouts := []GitCheckout{checkout("https://git/hera-2.git", "52d790ee", "develop")}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryUnresolved, libs[0].Resolution)
	assert.Contains(t, libs[0].Reason, "no git checkout recorded the branch \"v1.4.2\"")
	assert.Empty(t, checkouts[0].Library)
}

// Ref names are compared verbatim: "refs/remotes/origin/develop" is not
// "develop". Normalising them would also fold an unrelated pipeline checkout
// into the library's evidence.
func TestResolveLibrarySHAsDoesNotNormaliseRefNames(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}
	checkouts := []GitCheckout{checkout("https://git/hera-2.git", "52d790ee", "refs/remotes/origin/develop")}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryUnresolved, libs[0].Resolution)
	assert.Empty(t, libs[0].SHA1)
}

// "No checkout at all" and "no checkout on that branch" are different failures
// and lead the reader somewhere different.
func TestResolveLibrarySHAsDistinguishesNoCheckoutData(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}

	ResolveLibrarySHAs(libs, nil)

	require.Equal(t, LibraryUnresolved, libs[0].Resolution)
	assert.Contains(t, libs[0].Reason, "no git checkout at all")
}

// A commit that is the head of several branches carries all their names.
func TestResolveLibrarySHAsMatchesAnyRecordedBranchName(t *testing.T) {
	libs := []SharedLibrary{lib("hera2", "develop")}
	checkouts := []GitCheckout{checkout("https://git/hera-2.git", "52d790ee", "main", "develop")}

	ResolveLibrarySHAs(libs, checkouts)

	assert.Equal(t, LibraryMatchedBranch, libs[0].Resolution)
	assert.Equal(t, "52d790ee", libs[0].SHA1)
}

func TestMovingLibrariesCountsRefRequests(t *testing.T) {
	src := &BuildSources{Libraries: []SharedLibrary{
		{Resolution: LibraryPinned},
		{Resolution: LibraryMatchedBranch},
		{Resolution: LibraryUnresolved},
	}}
	assert.Equal(t, 2, src.MovingLibraries())
}
