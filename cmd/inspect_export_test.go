package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const (
	multibranchClass = "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject"
	folderClass      = "com.cloudbees.hudson.plugins.folder.Folder"
	pipelineClass    = "org.jenkinsci.plugins.workflow.job.WorkflowJob"
)

// captureStderr mirrors captureStdout. The export summary is the payload of the
// command and it goes to stderr, so stdout stays clean.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(done)
	}()

	fn()

	_ = w.Close()
	os.Stderr = old
	<-done
	return buf.String()
}

// exportServer answers the folder tree with jobs and every config.xml with a
// body naming the escaped path it was requested on, so a test can tell which
// job's config landed in which directory. Jobs listed in fail are answered 500.
func exportServer(t *testing.T, jobs []map[string]any, fail map[string]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case strings.HasSuffix(path, "/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
		case strings.HasSuffix(path, "/config.xml"):
			if fail[path] {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte("<config>" + path + "</config>"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func job(name, class string, children ...map[string]any) map[string]any {
	j := map[string]any{"name": name, "_class": class}
	if len(children) > 0 {
		j["jobs"] = children
	}
	return j
}

// runExport executes the export against srv and returns what went to stderr.
func runExport(t *testing.T, srv *httptest.Server, dir string, args ...string) (string, error) {
	t.Helper()
	setupTestConfig(t, srv.URL)
	var err error
	stderr := captureStderr(t, func() {
		_, err = executeCmd(t, append([]string{"inspect", "SANDBOXES", "--xml", "--recursive", "-d", dir}, args...)...)
	})
	return stderr, err
}

func TestInspectExportMirrorsTheFolderTree(t *testing.T) {
	srv := exportServer(t, []map[string]any{
		job("Gecko-vemb", multibranchClass, job("feature%2Fbuild", pipelineClass)),
		job("team folder", folderClass, job("my app", pipelineClass)),
	}, nil)
	defer srv.Close()
	dir := t.TempDir()

	stderr, err := runExport(t, srv, dir)
	require.NoError(t, err)

	// The path each file records is the one the request went out on, so this
	// asserts the directory layout and the wire encoding at the same time.
	for wantDir, wantPath := range map[string]string{
		"":                                     "/job/SANDBOXES/config.xml",
		"Gecko-vemb":                           "/job/SANDBOXES/job/Gecko-vemb/config.xml",
		"Gecko-vemb/feature%2Fbuild":           "/job/SANDBOXES/job/Gecko-vemb/job/feature%252Fbuild/config.xml",
		"team folder":                          "/job/SANDBOXES/job/team%20folder/config.xml",
		filepath.Join("team folder", "my app"): "/job/SANDBOXES/job/team%20folder/job/my%20app/config.xml",
	} {
		body, readErr := os.ReadFile(filepath.Join(dir, wantDir, "config.xml"))
		require.NoError(t, readErr, "no config.xml exported for %q", wantDir)
		assert.Equal(t, "<config>"+wantPath+"</config>", string(body))
	}

	// A branch job's name carries the slash already encoded, and the encoded
	// form is the name: decoding it here would nest the branch under a
	// directory indistinguishable from a folder called "feature".
	_, statErr := os.Stat(filepath.Join(dir, "Gecko-vemb", "feature"))
	assert.True(t, os.IsNotExist(statErr), "%%2F must not be decoded into nested directories")

	assert.Contains(t, stderr, "Wrote 5 config.xml files")
	assert.Contains(t, stderr, dir)
	assert.Contains(t, stderr, "unredacted")
}

// The exported directory name is what you feed back to jkit inspect, which is
// the whole reason it is the job name verbatim.
func TestInspectExportedBranchDirectoryRoundTrips(t *testing.T) {
	srv := exportServer(t, []map[string]any{
		job("Gecko-vemb", multibranchClass, job("feature%2Fbuild", pipelineClass)),
	}, nil)
	defer srv.Close()
	dir := t.TempDir()

	_, err := runExport(t, srv, dir)
	require.NoError(t, err)

	entries, err := os.ReadDir(filepath.Join(dir, "Gecko-vemb"))
	require.NoError(t, err)
	var branchDir string
	for _, e := range entries {
		if e.IsDir() {
			branchDir = e.Name()
		}
	}
	require.NotEmpty(t, branchDir)

	setupTestConfig(t, srv.URL)
	out, err := executeCmd(t, "inspect", "SANDBOXES/Gecko-vemb/"+branchDir, "--xml")
	require.NoError(t, err)
	assert.Equal(t, "<config>/job/SANDBOXES/job/Gecko-vemb/job/feature%252Fbuild/config.xml</config>", out)
}

// Job names are user data on the Jenkins side and this writes files with them.
func TestInspectExportRefusesToWriteOutsideTheOutputDirectory(t *testing.T) {
	srv := exportServer(t, []map[string]any{
		job("../escape", pipelineClass),
		job("..", folderClass, job("nested", pipelineClass)),
		job("good", pipelineClass),
	}, nil)
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "out")

	stderr, err := runExport(t, srv, dir)
	require.Error(t, err, "an export that skipped jobs must not report success")

	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	assert.Equal(t, []string{"out"}, names, "nothing may be written outside -d")

	// The rest of the export still runs, and what was left out is named.
	assert.FileExists(t, filepath.Join(dir, "good", "config.xml"))
	assert.Contains(t, stderr, "Skipped 2:")
	assert.Contains(t, stderr, "escape")
	assert.Contains(t, stderr, "anything under it was not written")
}

// One unreadable job out of twenty-five must not abandon the other twenty-four.
func TestInspectExportReportsPerJobFailuresWithoutStopping(t *testing.T) {
	srv := exportServer(t,
		[]map[string]any{
			job("locked", pipelineClass),
			job("readable", pipelineClass),
		},
		map[string]bool{"/job/SANDBOXES/job/locked/config.xml": true})
	defer srv.Close()
	dir := t.TempDir()

	stderr, err := runExport(t, srv, dir)

	assert.FileExists(t, filepath.Join(dir, "readable", "config.xml"))
	assert.NoFileExists(t, filepath.Join(dir, "locked", "config.xml"))
	assert.Contains(t, stderr, "Wrote 2 config.xml files")
	assert.Contains(t, stderr, "Skipped 1:")
	assert.Contains(t, stderr, "SANDBOXES/locked")

	var exitErr *jenkins.ExitError
	require.True(t, errors.As(err, &exitErr), "a partial export must exit non-zero: %v", err)
	assert.Equal(t, 1, exitErr.Code)
}

// A flag accepted and then ignored is what the jk-2gc.5 checkpoint set out to
// stop, and --recursive adds four more ways to write one.
func TestInspectRejectsIncompleteExportFlags(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"recursive without xml":     {[]string{"--recursive", "-d", "/tmp/x"}, "--recursive exports raw config.xml files, so it needs --xml"},
		"out-dir without xml":       {[]string{"-d", "/tmp/x"}, "so it needs both"},
		"recursive without dir":     {[]string{"--xml", "--recursive"}, "it needs -d DIR"},
		"out-dir without recursive": {[]string{"--xml", "-d", "/tmp/x"}, "so it needs --recursive"},
		"output with recursive":     {[]string{"--xml", "--recursive", "-d", "/tmp/x", "-o", "/tmp/one.xml"}, "none of the others can be"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setupTestConfig(t, "https://jenkins.example.com")
			_, err := executeCmd(t, append([]string{"inspect", "team/svc"}, tt.args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NoDirExists(t, "/tmp/x")
		})
	}
}
