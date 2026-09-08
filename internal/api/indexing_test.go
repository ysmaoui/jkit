package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	multibranchClass = "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject"
	orgFolderClass   = "jenkins.branch.OrganizationFolder"
	folderClass      = "com.cloudbees.hudson.plugins.folder.Folder"
	pipelineClass    = "org.jenkinsci.plugins.workflow.job.WorkflowJob"
)

// jobServer answers /api/json for each job path with the given class, and the
// indexing log endpoints with the given text.
func jobServer(t *testing.T, classes map[string]string, logs map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := logs[r.URL.Path]; ok {
			w.Header().Set("X-Text-Size", strconv.Itoa(len(body)))
			_, _ = w.Write([]byte(body))
			return
		}
		if class, ok := classes[r.URL.Path]; ok {
			_, _ = w.Write([]byte(class))
			return
		}
		http.NotFound(w, r)
	}))
}

func classJSON(class string, children ...string) string {
	var b strings.Builder
	b.WriteString(`{"_class":"` + class + `","jobs":[`)
	for i, c := range children {
		if i > 0 {
			b.WriteString(",")
		}
		parts := strings.SplitN(c, "=", 2)
		b.WriteString(`{"name":"` + parts[0] + `","_class":"` + parts[1] + `"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestScanTargetMultibranchUsesIndexing(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/job/svc/api/json": classJSON(multibranchClass),
	}, map[string]string{
		"/job/team/job/svc/indexing/logText/progressiveText": "Started by timer\n",
	})
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	target, err := client.ScanTarget("team/svc")
	require.NoError(t, err)
	assert.Equal(t, "multibranch pipeline", target.Kind)

	chunk, err := client.GetScanLog(target, 0)
	require.NoError(t, err)
	assert.Equal(t, "Started by timer\n", chunk.Text)
}

// An organization folder is a plain ComputedFolder: its run is published as
// /computation, not /indexing.
func TestScanTargetOrganizationFolderUsesComputation(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/acme/api/json": classJSON(orgFolderClass),
	}, map[string]string{
		"/job/acme/computation/logText/progressiveText": "Starting organization scan\n",
	})
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	target, err := client.ScanTarget("acme")
	require.NoError(t, err)
	assert.Equal(t, "organization folder", target.Kind)

	chunk, err := client.GetScanLog(target, 0)
	require.NoError(t, err)
	assert.Equal(t, "Starting organization scan\n", chunk.Text)
}

// A folder is the most likely wrong target, because it is the prefix of the
// path the user already types. Naming the multibranch jobs inside it turns the
// refusal into the next command to run.
func TestScanTargetFolderNamesMultibranchChildren(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/api/json": classJSON(folderClass,
			"svc="+multibranchClass, "legacy="+pipelineClass),
	}, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	_, err := client.ScanTarget("team")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a folder")
	assert.Contains(t, err.Error(), "jkit scan team/svc")
	assert.NotContains(t, err.Error(), "team/legacy")
}

func TestScanTargetEmptyFolderSaysSo(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/api/json": classJSON(folderClass, "legacy="+pipelineClass),
	}, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	_, err := client.ScanTarget("team")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No multibranch job directly inside it")
	assert.Contains(t, err.Error(), "jkit list --folder team")
}

// A branch child is created by indexing and never runs it. The error resolves
// the parent and hands back the command that does work.
func TestScanTargetBranchChildPointsAtParent(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/job/svc/job/feature%2Fx/api/json": classJSON(pipelineClass),
		"/job/team/job/svc/api/json":                 classJSON(multibranchClass),
	}, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	_, err := client.ScanTarget("team/svc/feature%2Fx")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a branch of team/svc")
	assert.Contains(t, err.Error(), "jkit scan team/svc --branch feature/x")
}

func TestScanTargetPlainJobSaysThereIsNoScan(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/job/legacy/api/json": classJSON(pipelineClass),
		"/job/team/api/json":            classJSON(folderClass),
	}, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	_, err := client.ScanTarget("team/legacy")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never indexes branches")
	assert.Contains(t, err.Error(), "jkit inspect team/legacy")
}

func TestScanTargetMissingJob(t *testing.T) {
	srv := jobServer(t, nil, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	_, err := client.ScanTarget("team/svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no job "team/svc"`)
	assert.Contains(t, err.Error(), "jkit list")
}

// A container that has never been scanned 404s exactly like a missing job.
// Saying "not found" there would send the reader after the wrong thing.
func TestScanLogNeverScannedIsNotAMissingJob(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/job/svc/api/json": classJSON(multibranchClass),
	}, nil)
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	target, err := client.ScanTarget("team/svc")
	require.NoError(t, err)

	_, err = client.GetScanLog(target, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never been scanned")
	assert.NotContains(t, err.Error(), "not found")
}

// A refusal must name the permission and the object it is needed on, so it is
// not read as a missing job or a missing plugin.
func TestScanLogPermissionErrorNamesThePermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/json") {
			_, _ = w.Write([]byte(classJSON(multibranchClass)))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	target, err := client.ScanTarget("team/svc")
	require.NoError(t, err)

	_, err = client.GetScanLogSize(target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/Read")
	assert.Contains(t, err.Error(), "team/svc")
}

func TestScanLogSize(t *testing.T) {
	srv := jobServer(t, map[string]string{
		"/job/team/job/svc/api/json": classJSON(multibranchClass),
	}, map[string]string{
		"/job/team/job/svc/indexing/logText/progressiveText": strings.Repeat("x", 4096),
	})
	defer srv.Close()
	client := NewClient(srv.URL, "admin", "secret")

	target, err := client.ScanTarget("team/svc")
	require.NoError(t, err)
	size, err := client.GetScanLogSize(target)
	require.NoError(t, err)
	assert.Equal(t, int64(4096), size)
}
