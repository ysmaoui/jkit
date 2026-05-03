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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jk/internal/jenkins"
)

func setupTestConfig(t *testing.T, host string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JK_CONFIG_DIR", dir)
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
	cmd.PersistentFlags().String("host", "", "Jenkins host URL")
	cmd.PersistentFlags().Bool("json", false, "Output as JSON")
	cmd.PersistentFlags().String("format", "", "Output format (Go template, use {{range .}}...{{end}} for lists)")
	cmd.PersistentFlags().Bool("no-color", false, "Disable color output")
	// Reset subcommand local flags to avoid state leaking between tests
	for _, sub := range cmd.Commands() {
		sub.ResetFlags()
	}
	// Re-register subcommand flags
	runCmd.Flags().StringArrayP("param", "p", nil, "Build parameter (KEY=VALUE)")
	runCmd.Flags().Bool("wait", false, "Wait for build to complete")
	runCmd.Flags().Bool("log", false, "Stream build log (implies --wait)")
	logCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	logCmd.Flags().String("stage", "", "Show log for a specific pipeline stage")
	logCmd.Flags().String("grep", "", "Filter log lines matching pattern")
	logCmd.Flags().BoolP("ignore-case", "i", false, "Case-insensitive --grep matching")
	listCmd.Flags().String("folder", "", "Folder path to list")
	statusCmd.Flags().Int("limit", 10, "Number of recent builds to show")
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
	t.Setenv("JK_CONFIG_DIR", dir)

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

// --- run ---

func TestRunCommandTrigger(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound) // CSRF disabled
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
	t.Setenv("JK_CONFIG_DIR", dir)
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
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5")
	require.NoError(t, err)
	assert.Contains(t, out, "hello world")
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
	t.Setenv("JK_CONFIG_DIR", dir)
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
	// Without args and without .jk.yml / git, should fail with context error
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
