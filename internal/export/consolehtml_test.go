package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
)

// The fixtures in testdata/consolehtml are a finished build's progressiveHtml
// (<job>.html) and every flow node's CopyStepLog text (<job>.steps.json),
// captured from the integration Jenkins by TestCaptureConsoleHTML.
var consoleFixtures = []struct {
	job     string
	noNodes bool
}{
	// Timestamper, AnsiColor, entities, catchError, nested parallel stages,
	// and a step that writes nothing.
	{job: "console-pipeline"},
	// No Timestamper.
	{job: "test-pipeline"},
	// Freestyle: no node spans.
	{job: "test-job", noNodes: true},
}

func readConsoleFixture(t *testing.T, job string) (html []byte, steps map[string]string) {
	t.Helper()
	html, err := os.ReadFile(filepath.Join("testdata", "consolehtml", job+".html"))
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join("testdata", "consolehtml", job+".steps.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &steps))
	return html, steps
}

// splitConsole runs html through a consoleSplitter in the given writes.
func splitConsole(writes ...[]byte) (map[string]string, error) {
	got := map[string]*bytes.Buffer{}
	s := newConsoleSplitter(func(node string, text []byte) error {
		if got[node] == nil {
			got[node] = &bytes.Buffer{}
		}
		got[node].Write(text)
		return nil
	})
	for _, w := range writes {
		if _, err := s.Write(w); err != nil {
			return nil, err
		}
	}
	err := s.Close()
	out := map[string]string{}
	for k, v := range got {
		out[k] = v.String()
	}
	return out, err
}

func sanitized(t *testing.T, text string) string {
	t.Helper()
	var b bytes.Buffer
	s := &sanitizer{w: &b}
	_, err := s.Write([]byte(text))
	require.NoError(t, err)
	require.NoError(t, s.flush())
	return b.String()
}

func TestConsoleSplitterMatchesStepLogs(t *testing.T) {
	for _, f := range consoleFixtures {
		t.Run(f.job, func(t *testing.T) {
			html, steps := readConsoleFixture(t, f.job)
			got, err := splitConsole(html)
			if f.noNodes {
				require.ErrorIs(t, err, errNoNodeSpans)
				assert.Empty(t, steps)
				assert.Equal(t, []string{""}, slices.Collect(maps.Keys(got)))
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, steps)
			for id, want := range steps {
				assert.Equal(t, sanitized(t, want), sanitized(t, got[id]), "node %s", id)
			}
			for id := range got {
				if id != "" {
					assert.Contains(t, steps, id, "text for node %s, which has no step log", id)
				}
			}
		})
	}
}

// The build's own text is what is left: [Pipeline] lines and output outside
// any step.
func TestConsoleSplitterBuildText(t *testing.T) {
	html, _ := readConsoleFixture(t, "console-pipeline")
	got, err := splitConsole(html)
	require.NoError(t, err)
	build := got[""]
	assert.True(t, strings.HasPrefix(build, "Started by user admin\n[Pipeline] Start of Pipeline\n"), build)
	assert.True(t, strings.HasSuffix(build, "[Pipeline] End of Pipeline\nFinished: SUCCESS\n"), build)
	assert.NotContains(t, build, "about to fail")
	assert.NotContains(t, build, "<")
}

// The fixture shapes the captured markup must keep showing, so a re-capture
// that loses one fails here rather than passing on less.
func TestConsoleFixtureShapes(t *testing.T) {
	html, steps := readConsoleFixture(t, "console-pipeline")
	for _, want := range []string{
		`<span class="timestamp">`,
		`<span style="display: none">[`,
		"<!--\x1b[31m-->",
		"&lt; b &amp;&amp; c &gt;",
		"ERROR: script returned exit code 3",
	} {
		assert.Contains(t, string(html), want)
	}
	// A step that wrote nothing, and parallel branches interleaved.
	assert.Contains(t, slices.Collect(maps.Values(steps)), "")
	opened := map[string]int{}
	for _, m := range regexp.MustCompile(`pipeline-node-(\d+)"`).FindAllStringSubmatch(string(html), -1) {
		opened[m[1]]++
	}
	assert.Contains(t, slices.Collect(maps.Values(opened)), 3, "a node's span reopened after another node's")
}

// Every split point of the fixture into two writes, and byte-at-a-time,
// yields what one write does.
func TestConsoleSplitterSplitAnywhere(t *testing.T) {
	for _, f := range consoleFixtures {
		t.Run(f.job, func(t *testing.T) {
			html, _ := readConsoleFixture(t, f.job)
			want, wantErr := splitConsole(html)
			for i := 1; i < len(html); i++ {
				got, err := splitConsole(html[:i], html[i:])
				require.Equal(t, wantErr, err, "split at %d", i)
				require.Equal(t, want, got, "split at %d", i)
			}
			var bytewise [][]byte
			for i := range html {
				bytewise = append(bytewise, html[i:i+1])
			}
			got, err := splitConsole(bytewise...)
			require.Equal(t, wantErr, err)
			require.Equal(t, want, got)
		})
	}
}

func FuzzConsoleSplitter(f *testing.F) {
	f.Add([]byte(`<span class="pipeline-node-3"><span class="timestamp"><b>1</b> </span>a &amp; b</span>`), uint(5))
	f.Fuzz(func(t *testing.T, html []byte, at uint) {
		want, wantErr := splitConsole(html)
		i := int(at % uint(len(html)+1))
		got, err := splitConsole(html[:i], html[i:])
		require.Equal(t, wantErr, err)
		require.Equal(t, want, got)
	})
}

func TestConsoleSplitter(t *testing.T) {
	tests := map[string]struct {
		in   string
		want map[string]string
	}{
		"nested node spans": {
			`<span class="pipeline-node-1">a<span class="pipeline-node-2">b</span>c</span>d`,
			map[string]string{"1": "ac", "2": "b", "": "d"},
		},
		"timestamp dropped, hidden prefix kept": {
			`<span class="pipeline-node-1"><span class="timestamp"><b>11:00</b> </span><span style="display: none">[T]</span> x</span>`,
			map[string]string{"1": "[T] x"},
		},
		"numeric entities and unknown text": {
			`<span class="pipeline-node-1">&#39;&#x41;&amp &bogus; & x</span>`,
			map[string]string{"1": "'A& &bogus; & x"},
		},
		"comment with tag-like text": {
			`<span class="pipeline-node-1">a<!-- <span class="timestamp"> -- > -->b</span>`,
			map[string]string{"1": "ab"},
		},
		"quoted > in attribute": {
			`<span class="pipeline-node-1"><a href='x>y' class="pipeline-node-9">link</a></span>`,
			map[string]string{"1": "link"},
		},
		"stray close": {
			`</span>a<span class="pipeline-node-1">b</span>`,
			map[string]string{"": "a", "1": "b"},
		},
		"multi-class": {
			`<span class="x pipeline-node-7 y">a</span>`,
			map[string]string{"7": "a"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := splitConsole([]byte(tc.in))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConsoleSplitterStopsOnEmitError(t *testing.T) {
	boom := errors.New("boom")
	s := newConsoleSplitter(func(string, []byte) error { return boom })
	_, err := s.Write([]byte(`<span class="pipeline-node-1">a</span>`))
	require.ErrorIs(t, err, boom)
}

var flowNodeRe = regexp.MustCompile(`nodeId="(\d+)"|pipeline-node-(\d+)"`)

// TestCaptureConsoleHTML writes the fixtures from a running integration
// Jenkins whose seed jobs have each built once:
//
//	JKIT_CAPTURE_HOST=http://localhost:9090 go test ./internal/export -run TestCaptureConsoleHTML
func TestCaptureConsoleHTML(t *testing.T) {
	host := os.Getenv("JKIT_CAPTURE_HOST")
	if host == "" {
		t.Skip("JKIT_CAPTURE_HOST not set")
	}
	c := api.NewClient(host, "admin", "admin")
	ctx := context.Background()
	dir := filepath.Join("testdata", "consolehtml")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, f := range consoleFixtures {
		var html bytes.Buffer
		_, partial, err := c.CopyConsoleHTML(ctx, f.job, 1, &html)
		require.NoError(t, err)
		require.False(t, partial)
		steps := map[string]string{}
		for _, m := range flowNodeRe.FindAllStringSubmatch(html.String(), -1) {
			id := m[1] + m[2]
			var log bytes.Buffer
			_, _, err := c.CopyStepLog(ctx, f.job, 1, id, &log)
			require.NoError(t, err, fmt.Sprintf("%s node %s", f.job, id))
			steps[id] = log.String()
		}
		b, err := json.MarshalIndent(steps, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.job+".html"), html.Bytes(), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.job+".steps.json"), append(b, '\n'), 0o644))
	}
}

// A [Pipeline] line names a step only when it arrives whole up to its
// newline: "sh" cut from "sha256" must not count as an sh step.
func TestStepFnNeedsWholeLine(t *testing.T) {
	tests := map[string]struct {
		writes []string
		want   string
	}{
		"whole":        {[]string{"[Pipeline] sh\n"}, "sh"},
		"cut at end":   {[]string{"[Pipeline] sh", "\nout\n"}, ""},
		"cut in name":  {[]string{"[Pipeline] sh", "a256\n"}, ""},
		"later pieces": {[]string{"[Pipeline] echo\nhello\n"}, "echo"},
		"timestamper":  {[]string{`<span style="display: none">[2026-10-14T17:56:14.939Z]</span> [Pipeline] sh` + "\n"}, "sh"},
		"not a stamp":  {[]string{"[x] [Pipeline] sh\n"}, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var first []byte
			armed := false
			s := newConsoleSplitter(func(node string, text []byte) error {
				if node == "" && armed && first == nil {
					first = append([]byte{}, text...)
				}
				return nil
			})
			s.newNode = func(string, string, string) { armed = true }
			for i, w := range tt.writes {
				if i == 0 {
					w = `<span class="pipeline-new-node" nodeId="5">` + w
				}
				if i == len(tt.writes)-1 {
					w += `</span><span class="pipeline-node-5">out</span>`
				}
				_, err := s.Write([]byte(w))
				require.NoError(t, err)
			}
			require.NoError(t, s.Close())
			assert.Equal(t, tt.want, stepFn(first))
		})
	}
}
