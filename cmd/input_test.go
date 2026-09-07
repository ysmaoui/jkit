package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
)

type inputFixture struct {
	url   string
	posts []recordedPost
}

type recordedPost struct {
	path string
	form url.Values
}

// newInputFixture serves a job whose last build is #42, that build's input
// state, and records the decision POSTs.
func newInputFixture(t *testing.T, actions []map[string]any, postStatus int) *inputFixture {
	t.Helper()
	f := &inputFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/crumbIssuer/api/json":
			_ = json.NewEncoder(w).Encode(map[string]string{"crumb": "c", "crumbRequestField": "Jenkins-Crumb"})
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			f.posts = append(f.posts, recordedPost{path: r.URL.Path, form: form})
			w.WriteHeader(postStatus)
		case strings.HasSuffix(r.URL.Path, "/42/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"actions": actions})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "my-app", "lastBuild": map[string]any{"number": 42},
			})
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	setupTestConfig(t, srv.URL)
	return f
}

func inputAction(execs ...map[string]any) map[string]any {
	return map[string]any{
		"_class":          "org.jenkinsci.plugins.workflow.support.steps.input.InputAction",
		"waitingForInput": len(execs) > 0,
		"executions":      execs,
	}
}

func pendingExec(id, message string, params ...map[string]any) map[string]any {
	in := map[string]any{"message": message}
	if len(params) > 0 {
		in["parameters"] = params
	}
	return map[string]any{"id": id, "settled": false, "input": in}
}

func choiceParam(name, def string, choices ...string) map[string]any {
	return map[string]any{
		"_class": "hudson.model.ChoiceParameterDefinition", "name": name,
		"type": "ChoiceParameterDefinition", "description": "",
		"defaultParameterValue": map[string]any{"value": def}, "choices": choices,
	}
}

// --- listing ---

func TestInputListsPendingSteps(t *testing.T) {
	restricted := pendingExec("Promote", "Promote to prod?", choiceParam("TARGET", "staging", "staging", "prod"))
	restricted["input"].(map[string]any)["submitter"] = "alice"
	newInputFixture(t, []map[string]any{inputAction(restricted)}, http.StatusOK)

	out, err := executeCmd(t, "input", "my-app")
	require.NoError(t, err)
	assert.Contains(t, out, "Promote")
	assert.Contains(t, out, "Promote to prod?")
	assert.Contains(t, out, "TARGET")
	assert.Contains(t, out, "alice")
}

func TestInputListJSON(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "Promote to prod?"))}, http.StatusOK)

	out, err := executeCmd(t, "input", "my-app", "42", "--json")
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "Promote", got[0]["id"])
}

// Trap 2: an empty list is "nothing pending". It must never be reported as a
// missing plugin, and the two ordinary reasons for it read differently.
func TestInputEmptyListSaysNothingPendingNotMissingPlugin(t *testing.T) {
	cases := []struct {
		name    string
		actions []map[string]any
		want    string
	}{
		{"build never reached an input", []map[string]any{{"_class": "hudson.model.ParametersAction"}}, "never reached one"},
		{"input already answered", []map[string]any{inputAction()}, "have all been answered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newInputFixture(t, tc.actions, http.StatusOK)
			var err error
			stderr := captureStderr(t, func() { _, err = executeCmd(t, "input", "my-app", "42") })
			require.NoError(t, err)
			assert.Contains(t, stderr, "No input step waiting")
			assert.Contains(t, stderr, tc.want)
			assert.NotContains(t, strings.ToLower(stderr), "plugin")
		})
	}
}

// --- approving ---

func TestInputApproveWithoutParameters(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "Promote to prod?"))}, http.StatusOK)

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "input", "my-app", "42", "--approve") })
	require.NoError(t, err)
	require.Len(t, f.posts, 1)
	assert.Equal(t, "/job/my-app/42/input/Promote/proceedEmpty", f.posts[0].path)
	assert.Contains(t, stderr, "Approved")
}

// Trap 1: approving a step that declares parameters without supplying them
// would submit Jenkins' defaults. The refusal names the parameters, shows their
// defaults, and prints the command that would be correct.
func TestInputApproveRefusesToSubmitDefaults(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "Promote to prod?",
		choiceParam("TARGET", "staging", "staging", "prod"),
		map[string]any{"_class": "hudson.model.StringParameterDefinition", "name": "VERSION",
			"type": "StringParameterDefinition", "defaultParameterValue": map[string]any{"value": "1.0.0"}},
	))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve")

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "refusing to approve")
	assert.Contains(t, msg, "TARGET, VERSION")
	assert.Contains(t, msg, "staging")
	assert.Contains(t, msg, "1.0.0")
	assert.Contains(t, msg, "-p TARGET=<value> -p VERSION=<value>")
	assert.Empty(t, f.posts, "a refused approval must not reach Jenkins")
}

func TestInputApproveRefusesWhenOnlySomeParametersSupplied(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?",
		choiceParam("TARGET", "staging", "staging", "prod"),
		map[string]any{"name": "VERSION", "type": "StringParameterDefinition"},
	))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "-p", "TARGET=prod")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "VERSION")
	assert.Contains(t, err.Error(), "-p TARGET=prod")
	assert.Empty(t, f.posts)
}

func TestInputApproveWithAllParametersSubmits(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?",
		choiceParam("TARGET", "staging", "staging", "prod"),
	))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "-p", "TARGET=prod")

	require.NoError(t, err)
	require.Len(t, f.posts, 1)
	assert.Equal(t, "/job/my-app/42/input/Promote/proceed", f.posts[0].path)
	assert.JSONEq(t, `{"parameter":[{"name":"TARGET","value":"prod"}]}`, f.posts[0].form.Get("json"))
}

func TestInputApproveRejectsUnknownParameter(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?",
		choiceParam("TARGET", "staging", "staging", "prod"),
	))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "-p", "TARGE=prod")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no parameter "TARGE"`)
	assert.Empty(t, f.posts)
}

// --- denying ---

func TestInputDeny(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?"))}, http.StatusOK)

	var err error
	stderr := captureStderr(t, func() { _, err = executeCmd(t, "input", "my-app", "42", "--deny") })
	require.NoError(t, err)
	require.Len(t, f.posts, 1)
	assert.Equal(t, "/job/my-app/42/input/Promote/abort", f.posts[0].path)
	assert.Contains(t, stderr, "Denied")
}

func TestInputDenyRejectsParams(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?",
		choiceParam("TARGET", "staging", "staging", "prod")))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--deny", "-p", "TARGET=prod")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--param applies only to --approve")
	assert.Empty(t, f.posts)
}

func TestInputApproveAndDenyTogetherIsAnError(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?"))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "--deny")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use --approve and --deny together")
}

// --- choosing which input ---

func TestInputRequiresIDWhenSeveralArePending(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(
		pendingExec("Promote", "go?"), pendingExec("Rollback", "undo?"),
	)}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id")
	assert.Contains(t, err.Error(), "Promote, Rollback")
	assert.Empty(t, f.posts)
}

func TestInputSelectsByID(t *testing.T) {
	f := newInputFixture(t, []map[string]any{inputAction(
		pendingExec("Promote", "go?"), pendingExec("Rollback", "undo?"),
	)}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "--id", "Rollback")

	require.NoError(t, err)
	require.Len(t, f.posts, 1)
	assert.Equal(t, "/job/my-app/42/input/Rollback/proceedEmpty", f.posts[0].path)
}

// Trap 4: an input step ID is not a pipeline stage node ID, and a numeric --id
// is the tell that the two were confused.
func TestInputRejectsStageNodeIDAsInputID(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?"))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve", "--id", "17")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no pending input step "17"`)
	assert.Contains(t, err.Error(), "pending: Promote")
	assert.Contains(t, err.Error(), "stage node ID")
}

func TestInputUnknownNonNumericIDOmitsStageHint(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction(pendingExec("Promote", "go?"))}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--deny", "--id", "Promotee")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "stage node ID")
}

func TestInputDecideWithNothingPending(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction()}, http.StatusOK)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to approve")
}

// Trap 3: the refusal names who may answer the step.
func TestInputApproveRefusalNamesSubmitter(t *testing.T) {
	restricted := pendingExec("Promote", "go?")
	restricted["input"].(map[string]any)["submitter"] = "alice"
	newInputFixture(t, []map[string]any{inputAction(restricted)}, http.StatusForbidden)

	_, err := executeCmd(t, "input", "my-app", "42", "--approve")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "alice")
	assert.Contains(t, err.Error(), "restricts who may answer it")
}

// --- trap 6: --wait must not look like a hang ---

func TestWaitForBuildResultAnnouncesPausedBuild(t *testing.T) {
	buildPollInterval, inputPollInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() { buildPollInterval, inputPollInterval = 2*time.Second, 30*time.Second })

	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("tree"), "waitingForInput") {
			_ = json.NewEncoder(w).Encode(map[string]any{"actions": []map[string]any{
				inputAction(pendingExec("Promote", "Promote to prod?")),
			}})
			return
		}
		polls++
		body := map[string]any{"number": 42, "building": true}
		if polls > 3 {
			body = map[string]any{"number": 42, "building": false, "result": "SUCCESS", "duration": 1000}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	var err error
	stderr := captureStderr(t, func() {
		err = waitForBuildResult(context.Background(), api.NewClient(srv.URL, "u", "t"), "my-app", 42)
	})

	require.NoError(t, err)
	assert.Contains(t, stderr, "paused for input: Promote to prod?")
	assert.Contains(t, stderr, "jkit input my-app 42 --approve --id Promote")
	assert.Contains(t, stderr, "jkit input my-app 42 --deny --id Promote")
	assert.Equal(t, 1, strings.Count(stderr, "paused for input"), "each input is announced once")
}

func TestWaitForBuildResultStaysQuietWhenNothingIsPending(t *testing.T) {
	buildPollInterval, inputPollInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() { buildPollInterval, inputPollInterval = 2*time.Second, 30*time.Second })

	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("tree"), "waitingForInput") {
			_ = json.NewEncoder(w).Encode(map[string]any{"actions": []map[string]any{}})
			return
		}
		polls++
		body := map[string]any{"number": 42, "building": true}
		if polls > 2 {
			body = map[string]any{"number": 42, "building": false, "result": "FAILURE", "duration": 1000}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	var err error
	stderr := captureStderr(t, func() {
		err = waitForBuildResult(context.Background(), api.NewClient(srv.URL, "u", "t"), "my-app", 42)
	})

	require.Error(t, err)
	assert.NotContains(t, stderr, "paused for input")
}

func TestInputListJSONIsAlwaysAListWhenEmpty(t *testing.T) {
	newInputFixture(t, []map[string]any{inputAction()}, http.StatusOK)

	out, err := executeCmd(t, "input", "my-app", "42", "--json")
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(out))
}
