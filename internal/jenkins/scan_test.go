package jenkins

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// githubScanLog is the shape the GitHub branch source writes, reduced from a
// live log: a rejected branch, an accepted one that produced no build, a pull
// request and a tag.
const githubScanLog = `Started by timer
[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...
Connecting to https://github.example.com/api/v3 using app credentials
Examining ACME/widget

  Checking branches...

  Getting remote branches...

    Checking branch develop
      ‘Jenkinsfile’ not found
    Does not meet criteria

    Checking branch main
      ‘Jenkinsfile’ found
    Met criteria
Changes detected: main (null → 2f9b79d85a22087147758be978945dcbd86911d3)
No automatic build triggered for main

  2 branches were processed

  Checking pull-requests...

    Checking pull request #3
      ‘Jenkinsfile’ not found
    Does not meet criteria

  1 pull request were processed

  Checking tags...

    Checking tag v1.0.0
      ‘Jenkinsfile’ not found
    Does not meet criteria

  1 tag were processed

Finished examining ACME/widget

[Tue Sep 08 14:58:26 UTC 2026] Finished branch indexing. Indexing took 16 sec
Finished: SUCCESS
`

func TestParseScanLogReadsEveryHead(t *testing.T) {
	l := ParseScanLog(githubScanLog)

	require.Len(t, l.Heads, 4)
	assert.Equal(t, "Started by timer", l.Cause)
	assert.Equal(t, "Tue Sep 08 14:58:10 UTC 2026", l.StartedRaw)
	assert.Equal(t, "SUCCESS", l.Result)
	assert.True(t, l.Finished)
	assert.Equal(t, []string{"ACME/widget"}, l.Repos)

	assert.Equal(t, "branch", l.Heads[0].Kind)
	assert.Equal(t, "develop", l.Heads[0].Name)
	assert.Equal(t, CriteriaNotMet, l.Heads[0].Criteria)
	assert.Equal(t, "‘Jenkinsfile’ not found", l.Heads[0].Reason)
	assert.Equal(t, "ACME/widget", l.Heads[0].Repo)

	assert.Equal(t, CriteriaMet, l.Heads[1].Criteria)
	assert.Equal(t, []string{
		"Changes detected: main (null → 2f9b79d85a22087147758be978945dcbd86911d3)",
		"No automatic build triggered for main",
	}, l.Heads[1].Actions)

	assert.Equal(t, "pull request", l.Heads[2].Kind)
	assert.Equal(t, "#3", l.Heads[2].Name)
	assert.Equal(t, "tag", l.Heads[3].Kind)
	assert.Equal(t, "v1.0.0", l.Heads[3].Name)
}

// The block of a head is kept verbatim, because it is what --branch prints and
// the only output that does not depend on the parser being right.
func TestParseScanLogKeepsBlockVerbatim(t *testing.T) {
	l := ParseScanLog(githubScanLog)

	assert.Equal(t, []string{
		"    Checking branch develop",
		"      ‘Jenkinsfile’ not found",
		"    Does not meet criteria",
	}, l.Find("develop")[0].Lines)
}

// "Checking branches..." and "Checking pull-requests..." are section headers,
// not heads named "es..." — the log's own totals would disagree if they were
// counted, and the table would carry a phantom row.
func TestParseScanLogIgnoresSectionHeaders(t *testing.T) {
	l := ParseScanLog(githubScanLog)

	for _, h := range l.Heads {
		assert.NotContains(t, h.Name, "...")
	}
	assert.Empty(t, l.Stray)
	assert.Equal(t, map[string]int{"branch": 2, "tag": 1, "pull request": 1}, l.Parsed)
}

// Trap 1: an unfamiliar provider wording must surface. A verdict line the
// parser does not know is carried through verbatim and leaves the head marked
// unrecognized, instead of being dropped so the head reads as "no verdict".
func TestParseScanLogSurfacesUnknownVerdict(t *testing.T) {
	log := `[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...
Examining acme/widget

    Checking branch develop
      Rejected by the branch filter
  1 branch were processed
Finished: SUCCESS
`
	l := ParseScanLog(log)

	require.Len(t, l.Heads, 1)
	assert.Equal(t, CriteriaUnknown, l.Heads[0].Criteria)
	assert.Equal(t, []string{"Rejected by the branch filter"}, l.Heads[0].Unrecognized)
	assert.Equal(t, 1, l.UnrecognizedHeads())
}

// Trap 1: the log states its own totals, so a parse that missed a head can be
// caught without trusting the parse.
func TestCountDisagreementsCatchesMissedHeads(t *testing.T) {
	log := `[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...
Examining acme/widget

    Checking branch develop
      ‘Jenkinsfile’ not found
    Does not meet criteria

  4 branches were processed
Finished: SUCCESS
`
	l := ParseScanLog(log)

	require.Len(t, l.CountDisagreements(), 1)
	assert.Equal(t, "4 branches reported by the log, 1 parsed here", l.CountDisagreements()[0])

	assert.Empty(t, ParseScanLog(githubScanLog).CountDisagreements())
}

// A line outside every block that the parser does not know is kept too: an
// organization folder can record a verdict about a whole repository there.
func TestParseScanLogKeepsStrayLines(t *testing.T) {
	log := `[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...
Examining acme/widget
Evaluating orphaned items in acme » widget
Finished: SUCCESS
`
	l := ParseScanLog(log)

	assert.Equal(t, []string{"Evaluating orphaned items in acme » widget"}, l.Stray)
}

func TestParseScanLogMultipleRepositories(t *testing.T) {
	log := `[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...
Examining acme/one

    Checking branch main
    Met criteria

  1 branch were processed

Finished examining acme/one

Examining acme/two

    Checking branch main
    Does not meet criteria

  1 branch were processed

Finished examining acme/two
Finished: SUCCESS
`
	l := ParseScanLog(log)

	require.Len(t, l.Heads, 2)
	assert.Equal(t, "acme/one", l.Heads[0].Repo)
	assert.Equal(t, "acme/two", l.Heads[1].Repo)
	assert.Equal(t, []string{"acme/one", "acme/two"}, l.Repos)
	assert.Len(t, l.Find("main"), 2)
	assert.Equal(t, 2, l.Reported["branch"])
}

// Trap 2: the age of the scan is the difference between "your branch was
// rejected" and "your branch has not been looked at". It comes from the start
// line and nowhere else.
func TestScanLogAge(t *testing.T) {
	l := ParseScanLog(githubScanLog)
	require.NotNil(t, l.Started)

	now := time.Date(2026, 9, 8, 16, 58, 10, 0, time.UTC)
	age, ok := l.Age(now)
	require.True(t, ok)
	assert.Equal(t, 2*time.Hour, age)
}

// Go maps a zone abbreviation it cannot resolve to a zero offset, so a stamp
// written in a zone the reader's machine does not know would be read as UTC and
// the age reported hours off (a CEST stamp read on a UTC host). Reporting no
// age is the honest answer; the raw stamp is still printed.
func TestScanLogAgeUnknownForUnresolvableZone(t *testing.T) {
	l := ParseScanLog("[Tue Sep 08 14:58:10 ZZZ 2026] Starting branch indexing...\nFinished: SUCCESS\n")

	assert.Equal(t, "Tue Sep 08 14:58:10 ZZZ 2026", l.StartedRaw)
	assert.Nil(t, l.Started)
	_, ok := l.Age(time.Now())
	assert.False(t, ok)
}

func TestScanLogUnfinishedRun(t *testing.T) {
	l := ParseScanLog("[Tue Sep 08 14:58:10 UTC 2026] Starting branch indexing...\nExamining acme/widget\n")

	assert.False(t, l.Finished)
	assert.Empty(t, l.Result)
}

func TestScanLogSimilarNames(t *testing.T) {
	l := ParseScanLog(githubScanLog)

	assert.Equal(t, []string{"develop"}, l.Similar("develo"))
	assert.Empty(t, l.Similar("develop"), "an exact match is not a suggestion")
}

func TestScanLogEmptyLog(t *testing.T) {
	l := ParseScanLog("")

	assert.Empty(t, l.Heads)
	assert.Empty(t, l.StartedRaw)
	assert.True(t, l.BestEffort)
}
