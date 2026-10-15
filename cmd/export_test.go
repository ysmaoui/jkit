package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/export"
	"github.com/ysmaoui/jkit/internal/staplertest"
)

// htmlRun is a Jenkins with PGV where every job and build number is the same
// two-step pipeline, and #9 is each job's last build.
type htmlRun struct {
	building bool
	// failStep makes step 4's log and flow node 404, which export records.
	failStep bool
	// denySteps answers allSteps 403, which ends the export.
	denySteps bool
	// slowStep sends step 4's first line, then holds the body this long.
	slowStep time.Duration
	// prefix is a context path every route sits under, such as /jenkins.
	prefix string
	// onStep runs when step 4's log is requested, before it is answered.
	onStep func()

	mu     sync.Mutex
	builds []int
}

var htmlBuildPath = regexp.MustCompile(`^(?:/job/[^/]+)+/(\d+)/(.*)$`)

func (h *htmlRun) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, h.prefix+"/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, h.prefix)
		if h.onStep != nil && strings.HasPrefix(r.URL.Path, "/job/my-app/7/execution/node/4/log/") {
			h.onStep()
		}
		if r.URL.Path == "/api/json" {
			staplertest.WriteRoot(w, staplertest.Streaming)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/api/json") && !htmlBuildPath.MatchString(r.URL.Path) {
			_, _ = io.WriteString(w, `{"builds":[{"number":9,"result":"SUCCESS"}]}`)
			return
		}
		m := htmlBuildPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n, _ := strconv.Atoi(m[1])
		switch rest := m[2]; {
		case rest == "api/json":
			h.mu.Lock()
			h.builds = append(h.builds, n)
			h.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun", "number": n,
				"result": map[bool]any{true: nil, false: "SUCCESS"}[h.building], "building": h.building,
				"timestamp": 1791791175000, "url": fmt.Sprintf("http://jenkins/job/my-app/%d/", n),
			})
		case rest == "stages/tree":
			_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"complete":%t,"stages":[
				{"id":"3","name":"Build","type":"STAGE","state":"success","children":[]}]}}`, !h.building)
		case rest == "stages/allSteps" && h.denySteps:
			w.WriteHeader(http.StatusForbidden)
		case rest == "stages/allSteps":
			_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"runIsComplete":%t,"steps":[
				{"id":"4","name":"make all","title":"","state":"success","stageId":"3","startTimeMillis":1},
				{"id":"5","name":"done","title":"Print Message","state":"success","stageId":"3","startTimeMillis":2}]}}`, !h.building)
		case rest == "consoleText":
			staplertest.ConsoleText(w, "Started\nmake all\nFinished: SUCCESS\n")
		case strings.HasPrefix(rest, "execution/node/4/") && h.failStep:
			w.WriteHeader(http.StatusNotFound)
		case rest == "execution/node/4/log/logText/progressiveText" && h.slowStep > 0:
			w.Header().Set("X-Text-Size", "11")
			_, _ = io.WriteString(w, "first line\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(h.slowStep):
			}
		case rest == "execution/node/4/log/logText/progressiveText":
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "compiling\n", false)
		case rest == "execution/node/5/log/logText/progressiveText":
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "done\n", false)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL+h.prefix)
	t.Setenv("BUILD_URL", "")
	t.Setenv("JENKINS_URL", "")
	return srv.URL
}

func (h *htmlRun) requested() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.builds
}

// runHTMLExport runs jkit with args in a fresh working directory and returns
// stdout, stderr and the directory.
func runHTMLExport(t *testing.T, args ...string) (stdout, stderr, dir string, err error) {
	t.Helper()
	dir = t.TempDir()
	t.Chdir(dir)
	stderr = captureStderr(t, func() {
		stdout, err = executeCmd(t, args...)
	})
	return stdout, stderr, dir, err
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestExportHTMLJobAndBuild(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	out, stderr, dir, err := runHTMLExport(t, "export", "html", "my-app", "7")
	require.NoError(t, err)

	assert.Equal(t, "my-app-7.html\n", out, "stdout carries only the path")
	assert.Equal(t, []string{"my-app-7.html"}, dirNames(t, dir))
	body, err := os.ReadFile(filepath.Join(dir, "my-app-7.html"))
	require.NoError(t, err)
	assert.Contains(t, string(body), `id="snapshot"`)
	info, err := os.Stat(filepath.Join(dir, "my-app-7.html"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	assert.Contains(t, stderr, "fetching step 1/2: Build › make all\n")
	assert.Contains(t, stderr, "fetching step 2/2: Build › Print Message\n")
	assert.Contains(t, stderr, "fetching console log\n")
	assert.Contains(t, stderr, "exported 2 step logs and the console")
	assert.NotContains(t, stderr, "warning")
}

func TestExportHTMLJobOnlyTakesLastBuild(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	out, _, _, err := runHTMLExport(t, "export", "html", "my-app")
	require.NoError(t, err)
	assert.Equal(t, "my-app-9.html\n", out)
	assert.Equal(t, []int{9}, h.requested())
}

func TestExportHTMLFolderJobDefaultName(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	out, _, dir, err := runHTMLExport(t, "export", "html", "team/svc", "3")
	require.NoError(t, err)
	assert.Equal(t, "team_svc-3.html\n", out)
	assert.Equal(t, []string{"team_svc-3.html"}, dirNames(t, dir))
}

func TestExportHTMLBuildURL(t *testing.T) {
	h := &htmlRun{}
	host := h.serve(t)
	t.Setenv("BUILD_URL", host+"/job/my-app/5/")

	out, stderr, _, err := runHTMLExport(t, "export", "html")
	require.NoError(t, err)
	assert.Equal(t, "my-app-5.html\n", out)
	assert.Equal(t, []int{5}, h.requested())
	assert.Contains(t, stderr, "BUILD_URL")
}

func TestExportHTMLArgsOverrideBuildURL(t *testing.T) {
	h := &htmlRun{}
	host := h.serve(t)
	t.Setenv("BUILD_URL", host+"/job/other/5/")

	out, stderr, _, err := runHTMLExport(t, "export", "html", "my-app", "7")
	require.NoError(t, err)
	assert.Equal(t, "my-app-7.html\n", out)
	assert.Equal(t, []int{7}, h.requested())
	assert.NotContains(t, stderr, "BUILD_URL")
}

func TestExportHTMLOutputPath(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)
	target := filepath.Join(t.TempDir(), "run.html")

	out, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "-o", target)
	require.NoError(t, err)
	assert.Equal(t, target+"\n", out)
	assert.Equal(t, []string{"run.html"}, dirNames(t, filepath.Dir(target)))
}

func TestExportHTMLStdout(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	out, _, dir, err := runHTMLExport(t, "export", "html", "my-app", "7", "-o", "-")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "<!"), "stdout is the page: %.40q", out)
	assert.Contains(t, out, `id="snapshot"`)
	assert.Empty(t, dirNames(t, dir))
}

func TestExportHTMLStdoutRejectsJSON(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	_, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "-o", "-", "--json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-o -")
}

func TestExportHTMLFatalErrorLeavesNoFile(t *testing.T) {
	h := &htmlRun{denySteps: true}
	h.serve(t)

	out, _, dir, err := runHTMLExport(t, "export", "html", "my-app", "7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.Empty(t, out)
	assert.Empty(t, dirNames(t, dir))
}

// cancelExportOnStep cancels the export's signal context, as Ctrl-C or SIGTERM
// would, while step 4's log downloads, and reports the temp file then present.
func cancelExportOnStep(t *testing.T, h *htmlRun) *[]string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := exportSignalContext
	exportSignalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { exportSignalContext = old })
	var during []string
	h.slowStep = 5 * time.Second
	h.onStep = func() {
		during = dirNames(t, ".")
		cancel()
	}
	return &during
}

func TestExportHTMLInterruptLeavesNoFile(t *testing.T) {
	h := &htmlRun{}
	during := cancelExportOnStep(t, h)
	h.serve(t)

	_, _, dir, err := runHTMLExport(t, "export", "html", "my-app", "7")
	assert.Equal(t, 130, exitCode(t, err))
	// Where the OS cannot unlink an open file, the log spool shows here too.
	tmp := slices.DeleteFunc(slices.Clone(*during), func(name string) bool {
		return strings.HasPrefix(name, ".jkit-spool-")
	})
	require.Len(t, tmp, 1, "temp file exists while downloading: %v", *during)
	assert.True(t, strings.HasSuffix(tmp[0], ".tmp"), *during)
	assert.Empty(t, dirNames(t, dir))
}

func TestExportHTMLBuildURLUnderContextPath(t *testing.T) {
	h := &htmlRun{prefix: "/jenkins"}
	host := h.serve(t)
	t.Setenv("BUILD_URL", host+"/jenkins/job/my-app/5/")
	t.Setenv("JENKINS_URL", host+"/jenkins/")

	out, _, _, err := runHTMLExport(t, "export", "html")
	require.NoError(t, err)
	assert.Equal(t, "my-app-5.html\n", out)
	assert.Equal(t, []int{5}, h.requested())
}

func TestExportHTMLBuildURLUnderContextPathWithoutJenkinsURL(t *testing.T) {
	h := &htmlRun{prefix: "/jenkins"}
	host := h.serve(t)
	t.Setenv("BUILD_URL", host+"/jenkins/job/my-app/5/")

	out, _, _, err := runHTMLExport(t, "export", "html")
	require.NoError(t, err)
	assert.Equal(t, "my-app-5.html\n", out)
	assert.Equal(t, []int{5}, h.requested())
}

func TestExportHTMLBuildURLUnknownHostNamesJenkinsURLRoot(t *testing.T) {
	h := &htmlRun{prefix: "/jenkins"}
	h.serve(t)
	t.Setenv("BUILD_URL", "http://ci.internal:8080/ci/job/my-app/5/")
	t.Setenv("JENKINS_URL", "http://ci.internal:8080/ci/")

	_, _, _, err := runHTMLExport(t, "export", "html")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no credentials for "http://ci.internal:8080/ci"`)
}

func TestExportHTMLBuildURLWithHost(t *testing.T) {
	h := &htmlRun{prefix: "/jenkins"}
	host := h.serve(t)
	t.Setenv("BUILD_URL", "http://ci.internal:8080/ci/job/my-app/5/")
	t.Setenv("JENKINS_URL", "http://ci.internal:8080/ci/")

	out, _, _, err := runHTMLExport(t, "export", "html", "--host", host+"/jenkins")
	require.NoError(t, err)
	assert.Equal(t, "my-app-5.html\n", out)
	assert.Equal(t, []int{5}, h.requested())
}

func TestParseBuildURL(t *testing.T) {
	for _, tc := range []struct {
		buildURL, jenkinsURL string
		hosts                []string
		host, job            string
	}{
		{"https://x/jenkins/job/a/1/", "https://x/jenkins/", nil, "https://x/jenkins", "a"},
		{"https://x/jenkins/job/a/1/", "https://x/jenkins", nil, "https://x/jenkins", "a"},
		{"https://x/job/job/a/1/", "https://x/job/", nil, "https://x/job", "a"},
		{"https://x/job/a/1/", "", nil, "https://x", "a"},
		{"https://x/job/a/1/", "https://other/", nil, "https://x", "a"},
		{"https://x/jenkins/job/a/1/", "", []string{"https://x/jenkins/"}, "https://x/jenkins", "a"},
		{"https://x/jenkins/job/a/1/", "https://x/", []string{"https://x/jenkins"}, "https://x/jenkins", "a"},
	} {
		parsed, err := parseBuildURL(tc.buildURL, tc.jenkinsURL, tc.hosts)
		require.NoError(t, err, tc.buildURL)
		assert.Equal(t, tc.host, parsed.Host, tc.buildURL)
		assert.Equal(t, tc.job, parsed.JobPath, tc.buildURL)
		assert.Equal(t, 1, parsed.BuildNumber, tc.buildURL)
	}
}

func TestExportHTMLUnwritableDirFailsBeforeDownload(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	_, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "-o", filepath.Join(t.TempDir(), "missing", "x.html"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot write")
	assert.Empty(t, h.requested(), "no build read before the destination is known writable")
}

func TestExportHTMLMaxLogBytes(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	_, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "--max-log-bytes", "1000")
	require.NoError(t, err)

	for _, bad := range []string{"20MB", "-1"} {
		_, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "--max-log-bytes", bad)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "max-log-bytes", bad)
	}
}

func TestExportHTMLDefaultTimeout(t *testing.T) {
	old := exportTimeout
	exportTimeout = 100 * time.Millisecond
	t.Cleanup(func() { exportTimeout = old })

	h := &htmlRun{slowStep: 400 * time.Millisecond}
	h.serve(t)
	_, stderr, _, err := runHTMLExport(t, "export", "html", "my-app", "7")
	require.NoError(t, err)
	assert.Contains(t, stderr, "warning: step Build › make all:")
	assert.Contains(t, stderr, "--timeout")

	h = &htmlRun{slowStep: 400 * time.Millisecond}
	h.serve(t)
	_, stderr, _, err = runHTMLExport(t, "export", "html", "my-app", "7", "--timeout", "5s")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "warning", "an explicit --timeout replaces the export default")
}

func TestExportHTMLJSON(t *testing.T) {
	h := &htmlRun{failStep: true}
	h.serve(t)

	out, _, dir, err := runHTMLExport(t, "export", "html", "my-app", "7", "--json")
	require.NoError(t, err)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	info, err := os.Stat(filepath.Join(dir, "my-app-7.html"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"path": "my-app-7.html", "bytes": float64(info.Size()), "complete": true,
		"stepErrors": float64(1), "consoleError": false,
	}, res)
}

func TestExportHTMLFormat(t *testing.T) {
	h := &htmlRun{}
	h.serve(t)

	out, _, _, err := runHTMLExport(t, "export", "html", "my-app", "7", "--format", "{{.Path}} {{.Complete}}")
	require.NoError(t, err)
	assert.Equal(t, "my-app-7.html true", strings.TrimSpace(out))
}

func TestExportHTMLWarnings(t *testing.T) {
	h := &htmlRun{building: true, failStep: true}
	h.serve(t)

	out, stderr, _, err := runHTMLExport(t, "export", "html", "my-app", "7")
	require.NoError(t, err, "recorded step errors still export")
	assert.Equal(t, "my-app-7.html\n", out)
	assert.Contains(t, stderr, "warning: run still in progress; this is a snapshot at ")
	assert.Contains(t, stderr, "warning: step Build › make all: ")
}

func TestStepLabelShortensScripts(t *testing.T) {
	n := &export.Node{Name: "Build"}
	assert.Equal(t, "Build › Print Message", stepLabel(n, &export.Step{Name: "hi", Title: "Print Message"}))
	label := stepLabel(n, &export.Step{Name: "set -e\n" + strings.Repeat("x", 100)})
	assert.True(t, strings.HasPrefix(label, "Build › set -e xxx"), label)
	assert.Len(t, []rune(label), len([]rune("Build › "))+60)
}
