package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func setupTestConfig(t *testing.T, host string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	cfg := []byte("hosts:\n  " + host + ":\n    user: admin\n    token: secret\n    default: true\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), cfg, 0600))
}

// captureStdout redirects os.Stdout to a pipe, runs fn, then returns captured output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	// Read in goroutine to avoid deadlock on large output
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(done)
	}()

	fn()

	_ = w.Close()
	os.Stdout = old
	<-done
	return buf.String()
}

// executeCmd runs rootCmd with given args, returns stdout and error.
func executeCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var cmdErr error
	cmd := rootCmd
	// Reset persistent flags to defaults between test runs
	cmd.ResetFlags()
	registerRootFlags(cmd)
	// Reset subcommand local flags to avoid state leaking between tests
	for _, sub := range cmd.Commands() {
		sub.ResetFlags()
	}
	// Re-register subcommand flags
	runCmd.Flags().StringArrayP("param", "p", nil, "Build parameter (KEY=VALUE)")
	runCmd.Flags().Bool("wait", false, "Wait for build to complete")
	runCmd.Flags().Bool("log", false, "Stream build log (implies --wait)")
	registerLogFlags(logCmd)
	listCmd.Flags().String("folder", "", "Folder path to list")
	statusCmd.Flags().Int("limit", 10, "Number of recent builds to show")
	registerInspectFlags(inspectCmd)
	registerScanFlags(scanCmd)
	registerInputFlags(inputCmd)
	registerSourcesFlags(sourcesCmd)
	registerWaitFlags(waitCmd)
	cmd.SetArgs(args)
	out := captureStdout(t, func() {
		cmdErr = cmd.Execute()
	})
	return out, cmdErr
}

// --- list ---

func TestListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jobs": []map[string]any{
				{"name": "my-app", "url": "http://jenkins/job/my-app/", "color": "blue", "lastBuild": map[string]any{"number": 42, "result": "SUCCESS"}},
				{"name": "my-lib", "url": "http://jenkins/job/my-lib/", "color": "red", "lastBuild": map[string]any{"number": 10, "result": "FAILURE"}},
			},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "my-app")
	assert.Contains(t, out, "my-lib")
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "FAILURE")
}

func TestListCommandJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jobs": []map[string]any{
				{"name": "my-app", "url": "http://jenkins/job/my-app/", "color": "blue"},
			},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "list", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"name"`)
	assert.Contains(t, out, "my-app")
}

func TestListCommandFolder(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jobs": []map[string]any{
				{"name": "svc-a", "url": "http://jenkins/job/team/job/svc-a/", "color": "blue"},
			},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "list", "--folder", "team")
	require.NoError(t, err)
	assert.Contains(t, gotPath, "/job/team/api/json")
	assert.Contains(t, out, "svc-a")
}

func TestListCommandNoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)

	_, err := executeCmd(t, "list")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no hosts configured")
}

// --- status ---

func TestStatusCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"builds": []map[string]any{
				{"number": 5, "result": "SUCCESS", "building": false, "duration": 30000, "timestamp": 1700000000000},
				{"number": 4, "result": "FAILURE", "building": false, "duration": 15000, "timestamp": 1699900000000},
			},
		})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "status", "my-app")
	require.NoError(t, err)
	assert.Contains(t, out, "5")
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "4")
	assert.Contains(t, out, "FAILURE")
}

func TestStatusCommandSingleBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/3/api/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 3, "result": "SUCCESS", "building": false,
				"duration": 45000, "timestamp": 1700000000000,
				"url": "http://jenkins/job/my-app/3/",
			})
			return
		}
		// Blue Ocean stages endpoint — return 404
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "status", "my-app", "3")
	require.NoError(t, err)
	assert.Contains(t, out, "#3")
	assert.Contains(t, out, "SUCCESS")
}

func TestStatusCommandNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "status", "nonexistent")
	assert.Error(t, err)
}

// A URL target and a positional build number are both accepted; the build
// number may live in either.
func TestStatusURLTargetBuildNumber(t *testing.T) {
	cases := map[string][]string{
		"number as argument": {"/job/my-app/", "42"},
		"number in URL":      {"/job/my-app/42/"},
		"number in both":     {"/job/my-app/42/", "42"},
	}
	for name, suffix := range cases {
		t.Run(name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if strings.Contains(r.URL.Path, "/42/api/json") {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"number": 42, "result": "SUCCESS", "building": false,
						"duration": 45000, "timestamp": 1700000000000,
						"url": "http://jenkins/job/my-app/42/",
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()
			setupTestConfig(t, srv.URL)

			args := append([]string{"status", srv.URL + suffix[0]}, suffix[1:]...)
			out, err := executeCmd(t, args...)
			require.NoError(t, err)
			assert.Contains(t, paths, "/job/my-app/42/api/json")
			assert.Contains(t, out, "#42")
		})
	}
}

func TestStatusURLTargetBadBuildNumber(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"conflicting":  {[]string{"status", "https://jenkins.example.com/job/my-app/41/", "42"}, "conflicting build numbers"},
		"not a number": {[]string{"status", "https://jenkins.example.com/job/my-app/", "latest"}, "invalid build number"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setupTestConfig(t, "https://jenkins.example.com")
			_, err := executeCmd(t, tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// --- run ---

func TestRunCommandTrigger(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound) // CSRF disabled
			return
		}
		if r.URL.Path == "/job/my-app/api/json" {
			_, _ = w.Write([]byte(`{"property":[]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/build") && r.Method == "POST" {
			w.Header().Set("Location", srvURL+"/queue/item/42/")
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	srvURL = srv.URL
	setupTestConfig(t, srv.URL)

	// no --wait, so should return after trigger
	_, err := executeCmd(t, "run", "my-app")
	require.NoError(t, err)
}

func TestRunCommandWait(t *testing.T) {
	var srvURL string
	queueCalls := 0
	buildCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/job/my-app/api/json" {
			_, _ = w.Write([]byte(`{"property":[]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/build") && r.Method == "POST" {
			w.Header().Set("Location", srvURL+"/queue/item/10/")
			w.WriteHeader(http.StatusCreated)
			return
		}
		if strings.Contains(r.URL.Path, "/queue/item/10/api/json") {
			queueCalls++
			if queueCalls >= 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": 10, "executable": map[string]any{"number": 7, "url": srvURL + "/job/my-app/7/"},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 10, "why": "waiting"})
			}
			return
		}
		if strings.Contains(r.URL.Path, "/7/api/json") {
			buildCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7, "result": "SUCCESS", "building": false, "duration": 5000, "timestamp": 1700000000000,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	srvURL = srv.URL
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "run", "my-app", "--wait")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, queueCalls, 2)
	assert.GreaterOrEqual(t, buildCalls, 1)
}

func TestRunCommandWithParams(t *testing.T) {
	var srvURL string
	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/job/my-app/api/json" {
			_, _ = w.Write([]byte(`{"property":[]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/buildWithParameters") && r.Method == "POST" {
			gotPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.Header().Set("Location", srvURL+"/queue/item/99/")
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	srvURL = srv.URL
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "run", "my-app", "-p", "BRANCH=main", "-p", "ENV=staging")
	require.NoError(t, err)
	assert.Contains(t, gotPath, "buildWithParameters")
	assert.Contains(t, gotBody, "BRANCH=main")
	assert.Contains(t, gotBody, "ENV=staging")
}

// multibranchRunServer fakes a multibranch container: POST /build starts
// indexing and returns no Location header. posts collects POST paths.
func multibranchRunServer(t *testing.T, posts *[]string) *httptest.Server {
	t.Helper()
	return branchSourceRunServer(t, "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject", posts)
}

// branchSourceRunServer fakes a branch-source container of the given class
// named my-app.
func branchSourceRunServer(t *testing.T, class string, posts *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/crumbIssuer/api/json":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/job/my-app/api/json":
			_, _ = w.Write([]byte(`{"_class":"` + class + `","property":[]}`))
		case r.URL.Path == "/job/my-app/buildWithParameters":
			w.WriteHeader(http.StatusNotFound) // containers have no such action
		case r.Method == "POST":
			*posts = append(*posts, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	setupTestConfig(t, srv.URL)
	return srv
}

func TestRunCommandMultibranchTriggersIndexing(t *testing.T) {
	var posts []string
	srv := multibranchRunServer(t, &posts)
	defer srv.Close()

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "run", "my-app") })
	require.NoError(t, err)
	assert.Equal(t, []string{"/job/my-app/build"}, posts)
	assert.Contains(t, stderr, "Scan triggered for my-app. Pass --branch <name> to build a branch; see 'jkit scan my-app' for the scan result.")
	assert.NotContains(t, stderr, "does not apply")
}

func TestRunCommandOrgFolderTriggersIndexing(t *testing.T) {
	var posts []string
	srv := branchSourceRunServer(t, "jenkins.branch.OrganizationFolder", &posts)
	defer srv.Close()

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "run", "my-app") })
	require.NoError(t, err)
	assert.Equal(t, []string{"/job/my-app/build"}, posts)
	assert.Contains(t, stderr, "Scan triggered for my-app. Pick a repository and branch: jkit run my-app/<repo> --branch <name>; see 'jkit scan my-app' for the scan result.")
}

func TestRunCommandOrgFolderRejectsParams(t *testing.T) {
	var posts []string
	srv := branchSourceRunServer(t, "jenkins.branch.OrganizationFolder", &posts)
	defer srv.Close()

	_, err := executeCmd(t, "run", "my-app", "-p", "ENV=prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target a branch job (my-app/<repo>/<branch>)")
	assert.NotContains(t, err.Error(), "--")
	assert.Empty(t, posts)
}

func TestRunCommandMultibranchWaitNote(t *testing.T) {
	var posts []string
	srv := multibranchRunServer(t, &posts)
	defer srv.Close()

	for _, flag := range []string{"--wait", "--log"} {
		posts = nil
		var err error
		stderr := captureStderr(t, func() { _, err = executeCmd(t, "run", "my-app", flag) })
		require.NoError(t, err, flag)
		assert.Equal(t, []string{"/job/my-app/build"}, posts, flag)
		assert.Contains(t, stderr, "Scan triggered for my-app.", flag)
		assert.Contains(t, stderr, "--wait and --log do not apply to a scan", flag)
	}
}

func TestRunCommandMultibranchRejectsParams(t *testing.T) {
	var posts []string
	srv := multibranchRunServer(t, &posts)
	defer srv.Close()

	_, err := executeCmd(t, "run", "my-app", "-p", "ENV=prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "takes no parameters")
	assert.Empty(t, posts)
}

func TestRunCommandExitError(t *testing.T) {
	// Verify ExitError is returned for build failures
	exitErr := &jenkins.ExitError{Code: 1, Message: "FAILURE"}
	assert.Equal(t, "FAILURE", exitErr.Error())
	assert.Equal(t, 1, exitErr.Code)

	var target *jenkins.ExitError
	assert.True(t, errors.As(exitErr, &target))
}

// --- lint ---

func TestLintCommandSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, "Jenkinsfile successfully validated.")
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	// Create temp Jenkinsfile
	dir := t.TempDir()
	jf := filepath.Join(dir, "Jenkinsfile")
	require.NoError(t, os.WriteFile(jf, []byte("pipeline { agent any }"), 0644))

	out, err := executeCmd(t, "lint", jf)
	require.NoError(t, err)
	assert.Contains(t, out, "successfully validated")
}

func TestLintCommandFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, "Errors encountered validating Jenkinsfile:\n  line 1: bad syntax")
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	dir := t.TempDir()
	jf := filepath.Join(dir, "Jenkinsfile")
	require.NoError(t, os.WriteFile(jf, []byte("bad pipeline"), 0644))

	_, err := executeCmd(t, "lint", jf)
	require.Error(t, err)
	var exitErr *jenkins.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 1, exitErr.Code)
	assert.Contains(t, exitErr.Message, "bad syntax")
}

func TestLintCommandMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"),
		[]byte("hosts:\n  http://localhost:\n    user: a\n    token: b\n    default: true\n"), 0600))

	_, err := executeCmd(t, "lint", "/nonexistent/Jenkinsfile")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reading")
}

// --- log ---

func TestLogCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"builds": []map[string]any{
					{"number": 5, "result": "SUCCESS", "building": false},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "logText") {
			w.Header().Set("X-Text-Size", "11")
			_, _ = fmt.Fprint(w, "hello world")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/consoleText") {
			_, _ = fmt.Fprint(w, "hello world")
			return
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "hello world")
}

// --- stage selection (duplicate names) ---

// parallelStagesServer serves a PGV tree with two parallel branches
// ("RemoteExec", "RemoteCache") that each contain a stage named
// "Run Bazel Build", plus per-node stage logs. execState/cacheState set the
// branch stage states (e.g. "running" vs "success").
func parallelStagesServer(t *testing.T, execState, cacheState string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/tree"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{
					"complete": true,
					"stages": []map[string]any{
						{"id": "1", "name": "Parallel", "type": "PARALLEL_BLOCK", "state": "success", "children": []map[string]any{
							{"id": "2", "name": "RemoteExec", "type": "PARALLEL", "state": execState, "children": []map[string]any{
								{"id": "4", "name": "Run Bazel Build", "type": "STAGE", "state": execState, "totalDurationMillis": 5000},
							}},
							{"id": "3", "name": "RemoteCache", "type": "PARALLEL", "state": cacheState, "children": []map[string]any{
								{"id": "5", "name": "Run Bazel Build", "type": "STAGE", "state": cacheState, "totalDurationMillis": 3000},
							}},
						}},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/stages/log"):
			switch r.URL.Query().Get("nodeId") {
			case "4":
				_, _ = fmt.Fprint(w, "remote exec branch log")
			case "5":
				_, _ = fmt.Fprint(w, "remote cache branch log")
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLogStageAmbiguousName(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Run Bazel Build")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	// Both candidates listed by qualified path + ID.
	assert.Contains(t, err.Error(), "RemoteExec/Run Bazel Build")
	assert.Contains(t, err.Error(), "RemoteCache/Run Bazel Build")
	assert.Contains(t, err.Error(), "id=4")
	assert.Contains(t, err.Error(), "id=5")
	assert.Contains(t, err.Error(), `pass a qualified path (e.g. "RemoteExec/Run Bazel Build") or --stage-id <id>`)
}

func TestLogStageQualifiedPath(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage", "RemoteExec/Run Bazel Build")
	require.NoError(t, err)
	assert.Contains(t, out, "remote exec branch log")
	assert.NotContains(t, out, "remote cache branch log")
}

func TestLogStageByID(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "remote cache branch log")
	assert.NotContains(t, out, "remote exec branch log")
}

func TestLogStageAndStageIDConflict(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "RemoteExec", "--stage-id", "4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use --stage and --stage-id together")
}

func TestLogStageNotFound(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage", "Nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Contains(t, err.Error(), "RemoteExec/Run Bazel Build")
	assert.Contains(t, err.Error(), "use --stage-id <id> for an exact node ID")
}

func TestLogStageFollow(t *testing.T) {
	// Stage already terminal → streamer prints once and exits.
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	old := stagePollInterval
	stagePollInterval = 5 * time.Millisecond
	defer func() { stagePollInterval = old }()

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Contains(t, out, "remote exec branch log")
}

// A running stage with -f --grep is read once and filtered, not followed. The
// stage stops reporting running after a few polls so a following regression
// fails the call count instead of hanging.
func TestLogStageFollowGrepReadsOnce(t *testing.T) {
	var logCalls int
	srv := stageLogServer(t, func(n int) string {
		logCalls = n
		return "alpha\nBETA one\ngamma\nbeta two\n"
	}, func(n int) bool { return n <= 3 })
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f", "--grep", "beta")
	require.NoError(t, err)
	assert.Equal(t, "beta two\n", out)

	out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f", "--grep", "beta", "-i")
	require.NoError(t, err)
	assert.Equal(t, "BETA one\nbeta two\n", out)
	assert.Equal(t, 2, logCalls)
}

// bigStageLogBody is a stage log well past the 64-byte cap the tests set,
// ending in a Bazel-style summary.
var bigStageLogBody = "first line\n" + strings.Repeat("filler line\n", 50) +
	"Build did NOT complete successfully\nsummary last line\n"

// stageLogServer serves stage 4 of build 5 through PGV. log returns the stage
// log for the n-th log request; running reports the stage and build state as of
// the n-th tree request.
func stageLogServer(t *testing.T, log func(n int) string, running func(n int) bool) *httptest.Server {
	t.Helper()
	var logCalls, treeCalls int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/tree"):
			treeCalls++
			state := "failure"
			if running(treeCalls) {
				state = "running"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{
					"complete": !running(treeCalls),
					"stages":   []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": state}},
				},
			})
		case strings.Contains(r.URL.Path, "/stages/log"):
			logCalls++
			_, _ = fmt.Fprint(w, log(logCalls))
		case strings.HasSuffix(r.URL.Path, "/5/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": running(treeCalls)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func shrinkStageLogCap(t *testing.T) {
	t.Helper()
	old := stageLogCap
	stageLogCap = 64
	t.Cleanup(func() { stageLogCap = old })
}

func bigStageLogServer(t *testing.T) *httptest.Server {
	t.Helper()
	shrinkStageLogCap(t)
	return stageLogServer(t, func(int) string { return bigStageLogBody }, func(int) bool { return false })
}

func TestLogStagePastCapWarns(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4") })
	require.NoError(t, err)
	assert.Equal(t, bigStageLogBody[:64], out)
	assert.Contains(t, stderr, "stage 4 log exceeds")
	assert.Contains(t, stderr, "use --tail N to read the end")
}

func TestLogStageHeadWithinCapDoesNotWarn(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--head", "1") })
	require.NoError(t, err)
	assert.Equal(t, "first line\n", out)
	assert.Empty(t, stderr)
}

func TestLogStageTailPastCap(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "2") })
	require.NoError(t, err)
	assert.Equal(t, "Build did NOT complete successfully\nsummary last line\n", out)
	assert.Empty(t, stderr)
}

func TestLogStageTailMoreLinesThanWindowWarns(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "100") })
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(out, "summary last line\n"))
	assert.Contains(t, stderr, "only 2 of 100 lines fit")
}

func TestLogStageTailGrepPastCap(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var err error
	stderr := captureStderr(t, func() {
		_, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "5", "--grep", "line")
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "--grep searched only the last")

	// Enough matches for --tail: nothing was missed that the output needed.
	stderr = captureStderr(t, func() {
		_, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "1", "--grep", "summary")
	})
	require.NoError(t, err)
	assert.Empty(t, stderr)
}

// stepsOnlyStageServer serves stage 4 only step by step: the PGV log endpoint
// is absent and Blue Ocean answers the node log with 500. Each step log is
// bigStageLogBody. running sets the stage state PGV's tree reports.
func stepsOnlyStageServer(t *testing.T, running bool) *httptest.Server {
	t.Helper()
	shrinkStageLogCap(t)
	state := "failure"
	if running {
		state = "running"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/stages/tree"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{
					"complete": !running,
					"stages":   []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": state}},
				},
			})
		case strings.HasSuffix(p, "/5/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": running})
		case strings.HasSuffix(p, "/nodes/4/log/"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(p, "/nodes/4/steps/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "7"}, {"id": "8"}})
		case strings.HasSuffix(p, "/steps/7/log/"), strings.HasSuffix(p, "/steps/8/log/"):
			_, _ = fmt.Fprint(w, bigStageLogBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLogStageStepsFallbackPastCapWarns(t *testing.T) {
	srv := stepsOnlyStageServer(t, false)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4") })
	require.NoError(t, err)
	assert.Equal(t, bigStageLogBody[:64], out)
	assert.Contains(t, stderr, "stage 4 log exceeds")

	stderr = captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "2") })
	require.NoError(t, err)
	assert.Equal(t, "Build did NOT complete successfully\nsummary last line\n", out)
	assert.Empty(t, stderr)
}

func TestLogStageStepsFallbackFollowRefusesRunningStage(t *testing.T) {
	srv := stepsOnlyStageServer(t, true)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "only available per step")
	assert.Contains(t, err.Error(), "use --tail after the stage finishes")
}

func TestLogStageStepsFallbackFollowPrintsFinishedStage(t *testing.T) {
	srv := stepsOnlyStageServer(t, false)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, bigStageLogBody[:64], out)
	assert.Contains(t, stderr, "stage 4 log exceeds")
}

func TestLogStageTimeoutHintsAtFlag(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "partial\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "--tail", "1", "--timeout", "100ms")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--timeout")
}

func TestLogStageFollowStopsAtCap(t *testing.T) {
	srv := bigStageLogServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, bigStageLogBody[:64], out)
	assert.Contains(t, stderr, "stopped following")
	assert.Contains(t, stderr, "--tail N")
}

func TestLogStageFollowPrintsLinesWrittenAsStageFinishes(t *testing.T) {
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	defer func() { stagePollInterval = old }()

	// The second log read already carries the final line, but the tree request
	// between the reads reports the stage finished.
	srv := stageLogServer(t,
		func(n int) string {
			if n == 1 {
				return "building\n"
			}
			return "building\nfinal line\n"
		},
		func(int) bool { return false })
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "building\nfinal line\n", out)
}

func TestLogStageFollowSkipsPGVPlaceholder(t *testing.T) {
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	defer func() { stagePollInterval = old }()

	// A stage with no step logs yet gets PGV's placeholder; it must not shift
	// the offset of the real log that follows.
	srv := stageLogServer(t,
		func(n int) string {
			if n == 1 {
				return "No logs found\n"
			}
			return "step output\n"
		},
		func(n int) bool { return n == 1 })
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "step output\n", out)
}

// blueOceanFollowServer serves stage 4 through Blue Ocean only, which reports
// a running stage as UNKNOWN. The build is building for the first two build
// lookups; each log request returns one more line.
func blueOceanFollowServer(t *testing.T) *httptest.Server {
	t.Helper()
	var logCalls, buildCalls int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "4", "displayName": "Build", "result": "UNKNOWN", "state": "RUNNING"}})
		case strings.HasSuffix(r.URL.Path, "/nodes/4/log/"):
			logCalls++
			for i := 1; i <= logCalls; i++ {
				_, _ = fmt.Fprintf(w, "line %d\n", i)
			}
		case strings.HasSuffix(r.URL.Path, "/5/api/json"):
			buildCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": buildCalls <= 2})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLogStageFollowBlueOceanUnknownKeepsFollowing(t *testing.T) {
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	defer func() { stagePollInterval = old }()

	srv := blueOceanFollowServer(t)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	// Three polls while building, then the final read after the build ends.
	assert.Equal(t, "line 1\nline 2\nline 3\nline 4\n", out)
}

// stageStateServer serves stage 4 of build 5 through PGV with a one-line log.
// states[n] is the PGV state for tree request n+1, the last one repeating.
// building is the build's flag throughout; buildCalls counts the build lookups.
func stageStateServer(t *testing.T, states []string, building bool, buildCalls *int) *httptest.Server {
	t.Helper()
	var treeCalls int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stages/tree"):
			state := states[min(treeCalls, len(states)-1)]
			treeCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data": map[string]any{
					"stages": []map[string]any{{"id": "4", "name": "Deploy", "type": "STAGE", "state": state}},
				},
			})
		case strings.Contains(r.URL.Path, "/stages/log"):
			_, _ = fmt.Fprint(w, "deploy log\n")
		case strings.HasSuffix(r.URL.Path, "/5/api/json"):
			*buildCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": building})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLogStageFollowStopsWhenBuildEndsUnderRunningStage(t *testing.T) {
	oldInterval, oldCheck := stagePollInterval, stuckStageCheckPolls
	stagePollInterval, stuckStageCheckPolls = time.Millisecond, 3
	defer func() { stagePollInterval, stuckStageCheckPolls = oldInterval, oldCheck }()

	var buildCalls int
	srv := stageStateServer(t, []string{"running"}, false, &buildCalls)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, "deploy log\n", out)
	assert.Contains(t, stderr, "build #5 has finished but stage 4 still reports IN_PROGRESS; stopped following")
	assert.Equal(t, 1, buildCalls, "only the third poll checks the build")
}

func TestLogStageFollowRunningSkipsBuildLookup(t *testing.T) {
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	defer func() { stagePollInterval = old }()

	var buildCalls int
	srv := stageStateServer(t, []string{"running", "paused", "queued", "success"}, true, &buildCalls)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "deploy log\n", out)
	assert.Zero(t, buildCalls)
}

func TestLogStageFollowNotBuiltNotesOnce(t *testing.T) {
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	defer func() { stagePollInterval = old }()

	var buildCalls int
	srv := stageStateServer(t, []string{"skipped", "skipped", "success"}, true, &buildCalls)
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(stderr, "stage 4 has not run (NOT_BUILT); following until it starts or the build ends"))
	assert.Equal(t, 2, buildCalls)
}

// stallingDiagnoseServer serves failed build 5 with one failed stage 4 whose
// log sends firstChunk, then stalls until release is closed.
func stallingDiagnoseServer(t *testing.T, release chan struct{}, firstChunk string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/5/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "result": "FAILURE"})
		case strings.HasSuffix(r.URL.Path, "/stages/tree"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data":   map[string]any{"stages": []map[string]any{{"id": "4", "name": "Compile", "type": "STAGE", "state": "failure"}}},
			})
		case strings.Contains(r.URL.Path, "/stages/log"):
			_, _ = fmt.Fprint(w, firstChunk)
			w.(http.Flusher).Flush()
			<-release
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestDiagnoseHintsAtTimeoutWhenStageLogStalls(t *testing.T) {
	shrinkStageLogCap(t)
	release := make(chan struct{})
	srv := stallingDiagnoseServer(t, release, strings.Repeat("progress\n", 20)+"ERROR: late failure\n")
	defer srv.Close()
	defer close(release)
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "diagnose", "my-app", "5", "--timeout", "200ms") })
	require.NoError(t, err)
	assert.Contains(t, out, "ERROR: late failure")
	assert.Contains(t, stderr, "warning: stage Compile: could not read the whole stage log")
	assert.Contains(t, stderr, "errors come from the part received before the read failed")
	assert.Contains(t, stderr, "--timeout")
}

func TestDiagnoseHintsAtTimeoutWhenBothStageLogReadsStall(t *testing.T) {
	release := make(chan struct{})
	srv := stallingDiagnoseServer(t, release, "")
	defer srv.Close()
	defer close(release)
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "diagnose", "my-app", "5", "--timeout", "200ms") })
	require.NoError(t, err)
	assert.Contains(t, out, "Compile")
	assert.Contains(t, stderr, "warning: stage Compile: could not read the stage log: ")
	assert.Contains(t, stderr, "--timeout")
}

func TestSanitizingLineWriterStripsSplitAnnotation(t *testing.T) {
	var out strings.Builder
	lw := &sanitizingLineWriter{w: &out}
	line := "a \x1b[8mha:AAAABBBB\x1b[0mb\nc"
	for i := 0; i < len(line); i += 3 {
		_, _ = lw.Write([]byte(line[i:min(i+3, len(line))]))
	}
	lw.Flush()
	assert.Equal(t, "a b\nc", out.String())
}

func TestSanitizingLineWriterBoundsUnterminatedLine(t *testing.T) {
	var out strings.Builder
	lw := &sanitizingLineWriter{w: &out}
	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 32; i++ {
		_, _ = lw.Write([]byte(chunk))
	}
	assert.LessOrEqual(t, len(lw.pending), maxPendingLine)
	lw.Flush()
	assert.Equal(t, 32*4096, out.Len())
}

// --- stages command ---

func TestStagesCommand(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "RemoteExec/Run Bazel Build")
	assert.Contains(t, out, "RemoteCache/Run Bazel Build")
	assert.Contains(t, out, "4")
	assert.Contains(t, out, "5")
}

func TestStagesCommandJSON(t *testing.T) {
	srv := parallelStagesServer(t, "success", "success")
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "stages", "my-app", "5", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"path"`)
	assert.Contains(t, out, "RemoteExec/Run Bazel Build")
	assert.Contains(t, out, `"id"`)
}

// --- auth status ---

func TestAuthStatusValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "NORMAL"})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "auth", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "Host:")
	assert.Contains(t, out, "Auth:  valid")
}

func TestAuthStatusInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "auth", "status")
	require.Error(t, err)
	var exitErr *jenkins.ExitError
	assert.True(t, errors.As(err, &exitErr))
}

func TestAuthStatusHostOverride(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "NORMAL"})
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "NORMAL"})
	}))
	defer srv2.Close()

	// Config with two hosts
	dir := t.TempDir()
	t.Setenv("JKIT_CONFIG_DIR", dir)
	cfg := fmt.Sprintf("hosts:\n  %s:\n    user: admin\n    token: secret\n    default: true\n  %s:\n    user: other\n    token: tok2\n", srv1.URL, srv2.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0600))

	out, err := executeCmd(t, "auth", "status", "--host", srv2.URL)
	require.NoError(t, err)
	assert.Contains(t, out, srv2.URL)
	assert.Contains(t, out, "other")
	assert.Contains(t, out, "Auth:  valid")
}

func TestAuthStatusHostOverrideNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "NORMAL"})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "auth", "status", "--host", "http://unknown:8080")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

// --- open ---

func TestOpenCommandNoArgs(t *testing.T) {
	// Without args and without .jkit.yml / git, should fail with context error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	// open with no args will try context resolution, which uses cwd
	// In test environment it should still resolve (dirname fallback)
	_, err := executeCmd(t, "open")
	// Either succeeds or gets an error from openBrowser — both are fine
	// The point is it doesn't fail with "requires at least 1 arg"
	if err != nil {
		assert.NotContains(t, err.Error(), "requires at least 1 arg")
	}
}

// stubBrowser puts no-op stand-ins for the platform browser launchers first on
// PATH so open tests do not spawn a real browser.
func stubBrowser(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"xdg-open", "open", "rundll32"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestOpenCommandURLTargetBuildNumber(t *testing.T) {
	tests := map[string]struct {
		args []string
		want string
	}{
		"number as argument": {[]string{"https://jenkins.example.com/job/my-app/", "42"}, "https://jenkins.example.com/job/my-app/42"},
		"number in URL":      {[]string{"https://jenkins.example.com/job/my-app/42/"}, "https://jenkins.example.com/job/my-app/42/"},
		"number in both":     {[]string{"https://jenkins.example.com/job/my-app/42/", "42"}, "https://jenkins.example.com/job/my-app/42/"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			stubBrowser(t)
			out, err := executeCmd(t, append([]string{"open"}, tt.args...)...)
			require.NoError(t, err)
			assert.Contains(t, out, "Opening "+tt.want+"\n")
		})
	}
}

func TestOpenCommandURLTargetBadBuildNumber(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"conflicting":  {[]string{"https://jenkins.example.com/job/my-app/41/", "42"}, "conflicting build numbers"},
		"not a number": {[]string{"https://jenkins.example.com/job/my-app/", "latest"}, "invalid build number"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			stubBrowser(t)
			_, err := executeCmd(t, append([]string{"open"}, tt.args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// --- ExitError ---

func TestExitErrorType(t *testing.T) {
	err := &jenkins.ExitError{Code: 2, Message: "UNSTABLE"}
	assert.Equal(t, "UNSTABLE", err.Error())
	assert.Equal(t, 2, err.Code)

	// Verify it works with errors.As
	var target *jenkins.ExitError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, 2, target.Code)
}

// --- host override ---

func TestHostOverrideNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "list", "--host", "http://other-host:8080")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

// --- filterLines ---

func TestFilterLines(t *testing.T) {
	text := "Hello World\nerror: something broke\nERROR: big problem\nall good\nfailed to compile\nFAILURE in test\n"

	tests := []struct {
		name       string
		pattern    string
		ignoreCase bool
		wantLines  []string
		wantAbsent []string
	}{
		{
			name:       "case-sensitive match",
			pattern:    "error",
			ignoreCase: false,
			wantLines:  []string{"error: something broke"},
			wantAbsent: []string{"ERROR: big problem"},
		},
		{
			name:       "case-insensitive match",
			pattern:    "error",
			ignoreCase: true,
			wantLines:  []string{"error: something broke", "ERROR: big problem"},
			wantAbsent: []string{"all good"},
		},
		{
			name:       "case-insensitive FAIL",
			pattern:    "fail",
			ignoreCase: true,
			wantLines:  []string{"failed to compile", "FAILURE in test"},
			wantAbsent: []string{"Hello World", "all good"},
		},
		{
			name:       "empty pattern returns all",
			pattern:    "",
			ignoreCase: false,
			wantLines:  []string{"Hello World", "error: something broke", "all good"},
		},
		{
			name:       "no matches returns empty",
			pattern:    "zzz_nonexistent",
			ignoreCase: false,
			wantAbsent: []string{"Hello", "error", "all good"},
		},
		{
			name:       "case-insensitive preserves original case in output",
			pattern:    "ERROR",
			ignoreCase: true,
			wantLines:  []string{"error: something broke", "ERROR: big problem"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterLines(text, tt.pattern, tt.ignoreCase)
			for _, want := range tt.wantLines {
				assert.Contains(t, got, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, got, absent)
			}
		})
	}
}

func TestFilterLinesEmptyInput(t *testing.T) {
	got := filterLines("", "pattern", false)
	assert.Empty(t, got)
}

func TestFilterLinesSingleLine(t *testing.T) {
	got := filterLines("hello world", "world", false)
	assert.Equal(t, "hello world\n", got)

	got = filterLines("hello world", "WORLD", false)
	assert.Empty(t, got)

	got = filterLines("hello world", "WORLD", true)
	assert.Equal(t, "hello world\n", got)
}

func TestApplyTailHead(t *testing.T) {
	input := "line1\nline2\nline3\nline4\nline5\n"

	// tail
	assert.Equal(t, "line4\nline5\n", applyTailHead(input, 2, 0))
	assert.Equal(t, "line5\n", applyTailHead(input, 1, 0))

	// head
	assert.Equal(t, "line1\nline2\n", applyTailHead(input, 0, 2))
	assert.Equal(t, "line1\n", applyTailHead(input, 0, 1))

	// both (tail first, then head)
	assert.Equal(t, "line4\n", applyTailHead(input, 2, 1))

	// no-op
	assert.Equal(t, input, applyTailHead(input, 0, 0))
	assert.Equal(t, input, applyTailHead(input, 10, 0))
	assert.Equal(t, input, applyTailHead(input, 0, 10))

	// empty
	assert.Equal(t, "", applyTailHead("", 5, 0))
	assert.Equal(t, "", applyTailHead("\n", 5, 0))
}

// --branch used to be dropped whenever the target was a URL: the flag parsed,
// was accepted, and did nothing. A URL naming a multibranch job plus a branch
// is the same request as the job-path form.
func TestBranchAppliesToAURLTarget(t *testing.T) {
	tests := map[string]struct {
		suffix, branch, wantPath string
	}{
		"url names the container": {
			"/job/team/job/svc/", "feature/x",
			"/job/team/job/svc/job/feature%2Fx/api/json",
		},
		"url already names the branch": {
			"/job/team/job/svc/job/feature%2Fx/", "feature/x",
			"/job/team/job/svc/job/feature%2Fx/api/json",
		},
		"branch without a slash": {
			"/job/team/job/svc/", "main",
			"/job/team/job/svc/job/main/api/json",
		},
		"branch given as its job name": {
			"/job/team/job/svc/", "feature%2Fx",
			"/job/team/job/svc/job/feature%2Fx/api/json",
		},
		"branch with hash given as its job name": {
			"/job/team/job/svc/", "feature%2Fx%234",
			"/job/team/job/svc/job/feature%2Fx%234/api/json",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				_ = json.NewEncoder(w).Encode(map[string]any{"builds": []map[string]any{
					{"number": 7, "result": "SUCCESS", "building": false},
				}})
			}))
			defer srv.Close()
			setupTestConfig(t, srv.URL)

			// r.URL.Path is decoded once, so the wire's %252F reads back as %2F.
			_, err := executeCmd(t, "status", srv.URL+tt.suffix, "--branch", tt.branch)
			require.NoError(t, err)
			assert.Contains(t, paths, tt.wantPath)
		})
	}
}

// Every way of naming a multibranch branch must reach the job branch-api
// created for it. Its name escapes # % / ? [ ] \ once, and the wire escapes it
// again, so the double-encoded classic URL spells the exact request path.
func TestBranchTargetsReachTheEncodedBranchJob(t *testing.T) {
	const project = "/job/INT/job/bsw-MPCI-BladeMain-stub"
	branches := map[string]struct{ raw, once, twice string }{
		"hash": {
			"feature/OVAPI_for_TIF_PI26.2BF#4",
			"feature%2FOVAPI_for_TIF_PI26.2BF%234",
			"feature%252FOVAPI_for_TIF_PI26.2BF%25234",
		},
		"question mark and percent": {"fix/50%?", "fix%2F50%25%3F", "fix%252F50%2525%253F"},
		"brackets and backslash":    {`a[1]\b`, "a%5B1%5D%5Cb", "a%255B1%255D%255Cb"},
		"space":                     {"my branch", "my%20branch", "my%20branch"},
	}
	for name, br := range branches {
		targets := map[string][]string{
			"blue ocean":             {"/blue/organizations/jenkins/INT%2Fbsw-MPCI-BladeMain-stub/detail/" + br.once + "/20/pipeline/"},
			"classic single-encoded": {project + "/job/" + br.once + "/20/"},
			"classic double-encoded": {project + "/job/" + br.twice + "/20/"},
			"branch flag":            {"INT/bsw-MPCI-BladeMain-stub", "20", "--branch", br.raw},
		}
		for form, args := range targets {
			t.Run(name+"/"+form, func(t *testing.T) {
				var paths []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.URL.EscapedPath())
					_ = json.NewEncoder(w).Encode(map[string]any{"number": 20, "result": "SUCCESS"})
				}))
				defer srv.Close()
				setupTestConfig(t, srv.URL)

				if strings.HasPrefix(args[0], "/") {
					args = []string{srv.URL + args[0]}
				}
				_, err := executeCmd(t, append([]string{"status"}, args...)...)
				require.NoError(t, err)
				assert.Contains(t, paths, project+"/job/"+br.twice+"/20/api/json")
			})
		}
	}
}

// jkit scan takes its own --branch, filtering the indexing log of the container
// it was given. The shared resolver must not rewrite the path underneath it.
func TestScanBranchDoesNotRewriteTheJobPath(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/api/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"_class": "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject",
				"name":   "svc",
			})
			return
		}
		_, _ = w.Write([]byte("Started branch indexing\nChecking branch develop\n  'Jenkinsfile' not found\nDoes not meet criteria\n"))
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, _ = executeCmd(t, "scan", srv.URL+"/job/team/job/svc/", "--branch", "develop")
	for _, p := range paths {
		assert.NotContains(t, p, "/job/develop", "scan filters the container's log; it must not append the branch to the path")
	}
}
