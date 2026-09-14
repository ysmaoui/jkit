package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const designDoc = "../../docs/DESIGN.md"

// This test exists because the docs checks in cmd/docs_test.go prove a name is
// present, not that anything useful is written about it. Three files once passed
// those while saying nothing about the config.xml commands. An endpoint is the
// part that is mechanically checkable: every Jenkins route the client builds
// should appear in the catalogue under "Jenkins API Patterns", so a new endpoint
// cannot ship undocumented.
//
// What it cannot catch: a path assembled from non-literal pieces is invisible
// here. SetJobEnabled builds "/"+verb from a variable, so /enable and /disable
// have to be kept in the catalogue by hand. Treat a green run as "no literal
// path is undocumented", not as "the catalogue is complete".

var (
	formatVerbRe  = regexp.MustCompile(`%[#+\-0-9.]*[a-zA-Z]`)
	placeholderRe = regexp.MustCompile(`\{[^}]*\}`)
	catalogueRe   = regexp.MustCompile(`^(?:GET|POST|PUT|DELETE|PATCH)\s+(\S+)`)
)

// pathSegments reduces a route to the segments that carry meaning: query
// strings, format verbs and {placeholders} all drop out, so a Go literal and a
// catalogue entry for the same endpoint reduce to the same thing.
func pathSegments(raw string) []string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = placeholderRe.ReplaceAllString(formatVerbRe.ReplaceAllString(raw, "*"), "*")
	var out []string
	for _, s := range strings.Split(raw, "/") {
		if s != "" && s != "*" {
			out = append(out, s)
		}
	}
	return out
}

// containsRun reports whether needle appears in hay as a contiguous run. Client
// code builds a route in pieces — "/build" is appended to a job path — so a
// literal is normally a fragment of the full endpoint, not the whole of it.
func containsRun(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if slicesEqual(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func slicesEqual(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// looksLikeRoute keeps request paths and rejects everything else a Go file
// holds: import paths and MIME types have no leading slash, and an error
// message that happens to start with a verb and contain a slash has spaces.
func looksLikeRoute(s string) bool {
	if !strings.Contains(s, "/") || strings.ContainsAny(s, " \t\n") {
		return false
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "%")
}

// clientRoutes returns every route literal in the package, keyed by its reduced
// form, with a source location for the failure message.
func clientRoutes(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	routes := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err, f)

		imported := map[*ast.BasicLit]bool{}
		for _, im := range node.Imports {
			imported[im.Path] = true
		}
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || imported[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !looksLikeRoute(s) {
				return true
			}
			segs := pathSegments(s)
			if len(segs) == 0 {
				return true
			}
			key := strings.Join(segs, "/")
			if _, seen := routes[key]; !seen {
				routes[key] = f + ": " + strconv.Quote(s)
			}
			return true
		})
	}
	return routes
}

func catalogueRoutes(t *testing.T) [][]string {
	t.Helper()
	body, err := os.ReadFile(designDoc)
	require.NoError(t, err)

	var out [][]string
	for _, line := range strings.Split(string(body), "\n") {
		if m := catalogueRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, pathSegments(m[1]))
		}
	}
	require.NotEmpty(t, out, "no GET/POST lines found in %s — has the catalogue moved?", designDoc)
	return out
}

func TestDesignDocumentsEveryClientRoute(t *testing.T) {
	catalogue := catalogueRoutes(t)
	routes := clientRoutes(t)
	require.NotEmpty(t, routes, "no route literals found — has the extraction broken?")

	var undocumented []string
	for key, where := range routes {
		documented := false
		for _, entry := range catalogue {
			if containsRun(entry, strings.Split(key, "/")) {
				documented = true
				break
			}
		}
		if !documented {
			undocumented = append(undocumented, where)
		}
	}
	sort.Strings(undocumented)

	if len(undocumented) > 0 {
		t.Errorf("%d route(s) the client builds are absent from the endpoint catalogue in %s.\n"+
			"Add each under \"Jenkins API Patterns\" as a GET/POST line, with the plugin it needs and any\n"+
			"behaviour a caller cannot guess:\n  %s",
			len(undocumented), designDoc, strings.Join(undocumented, "\n  "))
	}
}

func TestPathSegmentsReducesBothSpellingsAlike(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string
	}{
		{"go format string", "%s/%d/logText/progressiveText", []string{"logText", "progressiveText"}},
		{"catalogue entry", "/job/{path}/{number}/logText/progressiveText", []string{"job", "logText", "progressiveText"}},
		{"query string drops out", "/queue/cancelItem?id=%d", []string{"queue", "cancelItem"}},
		{"catalogue query drops out", "/queue/api/json?tree=items[id,why]", []string{"queue", "api", "json"}},
		{"trailing slash", "%s/%d/timestamps/", []string{"timestamps"}},
		{"all placeholders", "%s/%d", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pathSegments(tc.in))
		})
	}
}

// The literal a client builds is usually a fragment of the documented route, so
// matching is a contiguous run — but only contiguous, or an abbreviated
// catalogue entry would pass by accident.
func TestContainsRunRequiresAdjacency(t *testing.T) {
	entry := []string{"blue", "rest", "organizations", "jenkins", "pipelines", "runs", "nodes", "steps"}
	require.True(t, containsRun(entry, []string{"nodes", "steps"}))
	require.True(t, containsRun(entry, entry))
	require.False(t, containsRun(entry, []string{"pipelines", "nodes"}), "gap in the middle must not match")
	require.False(t, containsRun(entry, []string{"steps", "log"}), "longer than the entry must not match")
	require.False(t, containsRun(entry, nil))
}

// Only request paths. A Go file is full of other strings with slashes in them,
// and a false positive here would be a test failure nobody can act on.
func TestLooksLikeRouteRejectsNonPaths(t *testing.T) {
	for _, s := range []string{
		"%s/%d/api/json", "/queue/api/json", "/build",
	} {
		require.True(t, looksLikeRoute(s), s)
	}
	for _, s := range []string{
		"net/http", "encoding/json", "application/x-www-form-urlencoded", "://",
		"%s does not answer /%s even though it reports an enabled state",
	} {
		require.False(t, looksLikeRoute(s), s)
	}
}
