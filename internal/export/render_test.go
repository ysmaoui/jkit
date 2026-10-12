package export

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.html")

const hostileName = `echo "</script><!-- <b>bold</b> & 'quotes'"`

func mustLog(t *testing.T, maxBytes int64, text string) Log {
	t.Helper()
	l, err := encodeLog(maxBytes, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
	require.NoError(t, err)
	return l
}

// renderFixture covers every shape the page draws: a parallel stage whose
// branch nests stages, failed and error steps with exception text, a
// synthetic Post Actions, an UNLISTED stand-in, a running step with a partial
// log, a cut log, a step read error and a step name that tries to end the
// data block.
func renderFixture(t *testing.T) *Snapshot {
	t.Helper()
	const t0 = int64(1791791176000)
	var long strings.Builder
	for i := range 400 {
		long.WriteString("archiving file ")
		long.WriteString(strings.Repeat("x", i%40))
		long.WriteString("\n")
	}
	running := mustLog(t, 0, "starting integration run\nwaiting for service\n")
	running.Partial = true
	return &Snapshot{
		Job:              "team/app/main",
		Number:           42,
		Building:         true,
		StartTimeMillis:  t0,
		URL:              "https://jenkins.example.com/job/team/job/app/job/main/42/",
		ExportedAtMillis: t0 + 95_000,
		Stages: []Node{
			{ID: "6", Name: "Build", Type: "STAGE", State: "SUCCESS", StartTimeMillis: t0 + 100, DurationMillis: 12_300, Agent: "linux-1", Children: []Node{}, Steps: []Step{
				{ID: "8", Name: "make build\n./scripts/check.sh --all --verbose --with-a-very-long-argument-list-that-needs-truncation", State: "SUCCESS", StartTimeMillis: t0 + 200, DurationMillis: 12_000, Log: mustLog(t, 0, "go build ./...\nok\n")},
				{ID: "9", Name: hostileName, State: "SUCCESS", StartTimeMillis: t0 + 12_300, DurationMillis: 20, Log: mustLog(t, 0, "</script><!-- <b>bold</b> & 'quotes'\n")},
			}},
			{ID: "11", Name: "Test", Type: "STAGE", State: "IN_PROGRESS", Running: true, StartTimeMillis: t0 + 12_500, DurationMillis: 1000, Steps: []Step{}, Children: []Node{
				{ID: "14", Name: "unit", Type: "PARALLEL", State: "FAILURE", StartTimeMillis: t0 + 12_600, DurationMillis: 30_000, Children: []Node{}, Steps: []Step{
					{ID: "16", Name: "go test ./...", State: "FAILURE", StartTimeMillis: t0 + 12_700, DurationMillis: 29_000, Log: mustLog(t, 0, "--- FAIL: TestX (0.00s)\nFAIL\n"), Exception: "script returned exit code 1"},
					{ID: "17", Name: "boom", Title: "Error signal", State: "FAILURE", StartTimeMillis: t0 + 41_800, Exception: "boom"},
				}},
				{ID: "15", Name: "integration", Type: "PARALLEL", State: "IN_PROGRESS", Running: true, StartTimeMillis: t0 + 12_600, Steps: []Step{}, Children: []Node{
					{ID: "20", Name: "Setup", Type: "STAGE", State: "SUCCESS", StartTimeMillis: t0 + 12_700, DurationMillis: 3000, Children: []Node{}, Steps: []Step{
						{ID: "22", Name: "docker compose up -d", State: "SUCCESS", StartTimeMillis: t0 + 12_800, DurationMillis: 2900, Log: mustLog(t, 0, "Container db Started\n")},
					}},
					{ID: "25", Name: "Run", Type: "STAGE", State: "IN_PROGRESS", Running: true, StartTimeMillis: t0 + 15_800, Children: []Node{}, Steps: []Step{
						{ID: "27", Name: "./run-it.sh", State: "IN_PROGRESS", Running: true, StartTimeMillis: t0 + 15_900, Log: running},
					}},
				}},
			}},
			{ID: "40", Name: "Package", Type: "STAGE", State: "NOT_BUILT", Children: []Node{}, Steps: []Step{}},
			{ID: "55", Name: "Post Actions", Type: "STAGE", State: "SUCCESS", Synthetic: true, StartTimeMillis: t0 + 90_000, DurationMillis: 4000, Children: []Node{}, Steps: []Step{
				{ID: "57", Name: "**/*.log", Title: "Archive the artifacts", State: "SUCCESS", StartTimeMillis: t0 + 90_100, DurationMillis: 3000, Log: mustLog(t, 2000, long.String())},
				{ID: "58", Name: "", Title: "Delete workspace when build is done", State: "SUCCESS", StartTimeMillis: t0 + 93_200, DurationMillis: 100, Error: "Jenkins error HTTP 500 on /job/team/job/app/job/main/42/pipeline-overview/log"},
			}},
			{ID: "60", Name: "Node 60", Type: UnlistedType, Synthetic: true, Children: []Node{}, Steps: []Step{
				{ID: "61", Name: "cleanup", State: "SUCCESS", StartTimeMillis: t0 + 94_000, DurationMillis: 10, Log: mustLog(t, 0, "cleaned\n")},
			}},
		},
		Console: mustLog(t, 0, "Started by user admin\n[Pipeline] Start of Pipeline\nwarning: caught by catchError\n"),
	}
}

func renderString(t *testing.T, s *Snapshot) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, s))
	return buf.String()
}

func TestRenderGolden(t *testing.T) {
	got := renderString(t, renderFixture(t))
	path := "testdata/render.golden.html"
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/export -run TestRenderGolden -update")
	assert.Equal(t, string(want), got)
}

func TestRenderLoadsNothingFromNetwork(t *testing.T) {
	s := renderFixture(t)
	got := renderString(t, s)
	rest := strings.ReplaceAll(got, s.URL, "")
	assert.NotContains(t, rest, "http://")
	assert.NotContains(t, rest, "https://")
	assert.NotRegexp(t, `(?i)<script[^>]*\ssrc\s*=`, got)
	assert.NotRegexp(t, `(?i)<link[^>]*\shref\s*=`, got)
	assert.NotRegexp(t, `(?i)@import|\burl\(`, got)
	assert.NotRegexp(t, `fetch\(|XMLHttpRequest|WebSocket|EventSource|sendBeacon|import\(|(?i)<img|<iframe`, got)
}

var snapshotBlock = regexp.MustCompile(`(?s)<script type="application/json" id="snapshot">(.*?)</script>`)

func TestRenderEmbedsSnapshotSafely(t *testing.T) {
	s := renderFixture(t)
	s.Job = `evil</title><script>alert(1)</script>`
	got := renderString(t, s)

	// Only the data block and the page script close a script element.
	assert.Equal(t, 2, strings.Count(strings.ToLower(got), "</script"))
	assert.NotContains(t, got, "<!-- <b>")
	assert.Contains(t, got, "<title>evil&lt;/title&gt;&lt;script&gt;alert(1)&lt;/script&gt; #42</title>")

	m := snapshotBlock.FindStringSubmatch(got)
	require.NotNil(t, m)
	var back Snapshot
	require.NoError(t, json.Unmarshal([]byte(m[1]), &back))
	assert.Equal(t, hostileName, back.Stages[0].Steps[1].Name)
	assert.Equal(t, s.Job, back.Job)
	assert.Equal(t, *s, back)
}
