package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

func runningServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computer/api/json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	setupTestConfig(t, srv.URL)
	return srv
}

func runningBody(t *testing.T) string {
	now := time.Now().UnixMilli()
	b, err := json.Marshal(map[string]any{"computer": []map[string]any{{
		"oneOffExecutors": []map[string]any{
			{"currentExecutable": map[string]any{"url": "https://jenkins.example.com/job/team/job/svc/5/", "timestamp": now - 90_000}},
		},
		"executors": []map[string]any{
			{"currentExecutable": nil},
			{"currentExecutable": map[string]any{"url": "https://jenkins.example.com/job/tools/12/", "timestamp": now - 3_600_000}},
			{"currentExecutable": map[string]any{"url": "https://jenkins.example.com/job/team/job/svc/5/", "timestamp": now - 90_000}},
		},
	}}})
	require.NoError(t, err)
	return string(b)
}

func TestRunningTable(t *testing.T) {
	runningServer(t, runningBody(t))

	out, err := executeCmd(t, "running")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, out)
	assert.Regexp(t, `^JOB\s+BUILD\s+ELAPSED\s+URL\s*$`, lines[0])
	assert.Regexp(t, `^tools\s+#12\s+1h0m\s+https://jenkins.example.com/job/tools/12/\s*$`, lines[1])
	assert.Regexp(t, `^team/svc\s+#5\s+1m3\ds\s+https://jenkins.example.com/job/team/job/svc/5/\s*$`, lines[2])
}

func TestRunningUnknownStart(t *testing.T) {
	runningServer(t, `{"computer":[{"oneOffExecutors":[{"currentExecutable":{"url":"https://jenkins.example.com/job/app/2/","timestamp":0}}]}]}`)

	out, err := executeCmd(t, "running")
	require.NoError(t, err)
	assert.Regexp(t, `(?m)^app\s+#2\s+-\s+https://jenkins.example.com/job/app/2/\s*$`, out)
}

func TestRunningJSONAndFolder(t *testing.T) {
	runningServer(t, runningBody(t))

	out, err := executeCmd(t, "running", "--json", "--folder", "team")
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "team/svc", got[0]["job"])
	assert.EqualValues(t, 5, got[0]["number"])
	assert.Equal(t, "https://jenkins.example.com/job/team/job/svc/5/", got[0]["url"])
	assert.NotZero(t, got[0]["timestamp"])
}

func TestRunningFormat(t *testing.T) {
	runningServer(t, runningBody(t))

	out, err := executeCmd(t, "running", "--format", "{{range .}}{{.Job}} {{.Number}}\n{{end}}")
	require.NoError(t, err)
	assert.Equal(t, "tools 12\nteam/svc 5\n", out)
}

func TestRunningEmpty(t *testing.T) {
	runningServer(t, `{"computer":[{"oneOffExecutors":[],"executors":[{"currentExecutable":null}]}]}`)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "running") })
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "No running builds")

	out, err = executeCmd(t, "running", "--json")
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(out))
}

func TestRunningForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	_, err := executeCmd(t, "running")
	var pe *jenkins.PermissionError
	require.True(t, errors.As(err, &pe), "got %v", err)
}
