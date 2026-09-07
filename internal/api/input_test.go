package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

const inputActionClass = "org.jenkinsci.plugins.workflow.support.steps.input.InputAction"

// inputRun builds the /api/json body a WorkflowRun returns for the tree query
// in pendingInputTree, including the unexported actions that render as {}.
func inputRun(actions ...map[string]any) map[string]any {
	all := []map[string]any{{"_class": "hudson.model.ParametersAction"}, {}}
	all = append(all, actions...)
	return map[string]any{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun", "actions": all}
}

func pendingAction(execs ...map[string]any) map[string]any {
	return map[string]any{"_class": inputActionClass, "waitingForInput": len(execs) > 0, "executions": execs}
}

func execution(id string, input map[string]any) map[string]any {
	return map[string]any{"_class": "…InputStepExecution", "id": id, "settled": false, "input": input}
}

// inputServer answers the run's api/json with body and records every request.
type inputServer struct {
	*httptest.Server
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	form   url.Values
}

func newInputServer(t *testing.T, body any, status int) *inputServer {
	t.Helper()
	s := &inputServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_ = json.NewEncoder(w).Encode(crumbInfo{Crumb: "c", CrumbRequestField: "Jenkins-Crumb"})
			return
		}
		rec := recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query()}
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			rec.form, _ = url.ParseQuery(string(raw))
		}
		s.requests = append(s.requests, rec)

		if r.Method == http.MethodPost {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("<html><body>refused</body></html>"))
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *inputServer) posts() []recordedRequest {
	var out []recordedRequest
	for _, r := range s.requests {
		if r.method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

// --- reading pending input steps ---

func TestGetInputStateReadsPendingStep(t *testing.T) {
	body := inputRun(pendingAction(execution("Promote", map[string]any{
		"message":            "Promote to prod?",
		"ok":                 "Ship it",
		"submitter":          "alice, release-managers",
		"submitterParameter": "APPROVER",
		"parameters": []map[string]any{{
			"_class":                "hudson.model.ChoiceParameterDefinition",
			"name":                  "TARGET",
			"type":                  "ChoiceParameterDefinition",
			"description":           "where to deploy",
			"defaultParameterValue": map[string]any{"value": "staging"},
			"choices":               []string{"staging", "prod"},
		}},
	})))
	srv := newInputServer(t, body, http.StatusOK)

	state, err := NewClient(srv.URL, "u", "t").GetInputState("team/svc", 42)
	require.NoError(t, err)

	assert.True(t, state.ActionPresent)
	assert.True(t, state.WaitingForInput)
	require.Len(t, state.Pending, 1)
	in := state.Pending[0]
	assert.Equal(t, "Promote", in.ID)
	assert.Equal(t, "Promote to prod?", in.Message)
	assert.Equal(t, "Ship it", in.OK)
	assert.Equal(t, []string{"alice", "release-managers"}, in.Submitters())
	assert.Equal(t, "APPROVER", in.SubmitterParameter)
	require.Len(t, in.Parameters, 1)
	assert.Equal(t, "TARGET", in.Parameters[0].Name)
	assert.Equal(t, "staging", in.Parameters[0].DefaultString())
	assert.Equal(t, []string{"staging", "prod"}, in.Parameters[0].Choices())

	require.Len(t, srv.requests, 1)
	assert.Equal(t, "/job/team/job/svc/42/api/json", srv.requests[0].path)
	assert.Equal(t, pendingInputTree, srv.requests[0].query.Get("tree"))
}

// The _class field is the only one whose absence carries information, because
// Jenkins answers a tree query naming unknown fields with 200 and drops them.
func TestPendingInputTreeAsksForClass(t *testing.T) {
	assert.Contains(t, pendingInputTree, "actions[_class,")
}

// Trap 2: an empty result is "nothing pending", and the two ordinary reasons
// for it stay distinguishable. Neither is evidence of a missing plugin.
func TestGetInputStateEmptyWhenBuildNeverReachedAnInput(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)

	state, err := NewClient(srv.URL, "u", "t").GetInputState("team/svc", 24)
	require.NoError(t, err)
	assert.False(t, state.ActionPresent)
	assert.False(t, state.WaitingForInput)
	assert.Empty(t, state.Pending)
}

func TestGetInputStateEmptyWhenInputAlreadyAnswered(t *testing.T) {
	srv := newInputServer(t, inputRun(pendingAction()), http.StatusOK)

	state, err := NewClient(srv.URL, "u", "t").GetInputState("team/svc", 24)
	require.NoError(t, err)
	assert.True(t, state.ActionPresent)
	assert.False(t, state.WaitingForInput)
	assert.Empty(t, state.Pending)
}

func TestGetInputStateSkipsSettledExecutions(t *testing.T) {
	settled := execution("Old", map[string]any{"message": "already decided"})
	settled["settled"] = true
	srv := newInputServer(t, inputRun(pendingAction(settled, execution("New", map[string]any{"message": "still waiting"}))), http.StatusOK)

	state, err := NewClient(srv.URL, "u", "t").GetInputState("team/svc", 42)
	require.NoError(t, err)
	require.Len(t, state.Pending, 1)
	assert.Equal(t, "New", state.Pending[0].ID)
}

// --- approving ---

func TestApproveWithoutParametersUsesProceedEmpty(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)
	in := jenkins.PendingInput{ID: "Promote"}

	require.NoError(t, NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in, nil))

	posts := srv.posts()
	require.Len(t, posts, 1)
	assert.Equal(t, "/job/team/job/svc/42/input/Promote/proceedEmpty", posts[0].path)
}

// Trap 1: proceedEmpty on a step that declares parameters approves with the
// DEFAULTS. Nothing may reach that endpoint in that case — not even a request.
func TestApproveRefusesToSettleAStepWithUnsuppliedParameters(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)
	in := jenkins.PendingInput{ID: "Promote", Parameters: []jenkins.ParameterDefinition{
		{Name: "TARGET"}, {Name: "VERSION"},
	}}

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TARGET, VERSION")
	assert.Contains(t, err.Error(), "submits none of them")
	assert.Empty(t, srv.posts(), "no request may be sent when the approval is refused")
}

func TestApproveWithParametersPostsProceedForm(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)
	in := jenkins.PendingInput{ID: "Promote", Parameters: []jenkins.ParameterDefinition{
		{Name: "TARGET"}, {Name: "VERSION"},
	}}

	require.NoError(t, NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in,
		map[string]string{"TARGET": "prod", "VERSION": "1.4.2"}))

	posts := srv.posts()
	require.Len(t, posts, 1)
	assert.Equal(t, "/job/team/job/svc/42/input/Promote/proceed", posts[0].path)
	assert.NotContains(t, posts[0].path, "proceedEmpty")
	assert.JSONEq(t,
		`{"parameter":[{"name":"TARGET","value":"prod"},{"name":"VERSION","value":"1.4.2"}]}`,
		posts[0].form.Get("json"))
}

func TestApproveRejectsUndeclaredParameter(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)
	in := jenkins.PendingInput{ID: "Promote", Parameters: []jenkins.ParameterDefinition{{Name: "TARGET"}}}

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in, map[string]string{"TYPO": "prod"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no parameter "TYPO"`)
	assert.Empty(t, srv.posts())
}

// --- denying ---

func TestDenyPostsAbort(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusOK)

	require.NoError(t, NewClient(srv.URL, "u", "t").DenyInput("team/svc", 42, jenkins.PendingInput{ID: "Promote"}))

	posts := srv.posts()
	require.Len(t, posts, 1)
	assert.Equal(t, "/job/team/job/svc/42/input/Promote/abort", posts[0].path)
}

// --- refusals ---

// Trap 3: a step that names its submitters rejects everyone else regardless of
// Jenkins permissions, so the refusal names who is allowed.
func TestApproveRefusalNamesSubmitterWhenStepRestrictsIt(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusForbidden)
	in := jenkins.PendingInput{ID: "Promote", Submitter: "alice, release-managers"}

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in, nil)

	var decErr *jenkins.InputDecisionError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, jenkins.InputRefusedSubmitter, decErr.Reason)
	assert.Contains(t, err.Error(), "alice, release-managers")
	assert.NotContains(t, err.Error(), "Job/Build is required")
}

func TestApproveRefusalNamesPermissionWhenStepRestrictsNobody(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusForbidden)

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, jenkins.PendingInput{ID: "Promote"}, nil)

	var decErr *jenkins.InputDecisionError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, jenkins.InputRefusedPermission, decErr.Reason)
	assert.Contains(t, err.Error(), "Job/Build")
	assert.Contains(t, err.Error(), `"team/svc"`)
}

func TestDenyRefusalNamesCancelPermission(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusForbidden)

	err := NewClient(srv.URL, "u", "t").DenyInput("team/svc", 42, jenkins.PendingInput{ID: "Promote"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/Cancel")
}

// hudson.model.Failure renders an HTML error page rather than a 403, so a 4xx
// that is not 403 must classify the same way.
func TestApproveRefusalClassifiesNon403Rejection(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusBadRequest)
	in := jenkins.PendingInput{ID: "Promote", Submitter: "alice"}

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, in, nil)

	var decErr *jenkins.InputDecisionError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, jenkins.InputRefusedSubmitter, decErr.Reason)
}

func TestApproveOnVanishedInputReportsItAsGone(t *testing.T) {
	srv := newInputServer(t, inputRun(), http.StatusNotFound)

	err := NewClient(srv.URL, "u", "t").ApproveInput("team/svc", 42, jenkins.PendingInput{ID: "Promote"}, nil)

	var decErr *jenkins.InputDecisionError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, jenkins.InputRefusedGone, decErr.Reason)
	assert.Contains(t, err.Error(), "no longer pending")
}

func TestInputDecisionPostCarriesCrumb(t *testing.T) {
	var gotCrumb string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			_ = json.NewEncoder(w).Encode(crumbInfo{Crumb: "abc123", CrumbRequestField: "Jenkins-Crumb"})
			return
		}
		gotCrumb = r.Header.Get("Jenkins-Crumb")
	}))
	defer srv.Close()

	require.NoError(t, NewClient(srv.URL, "u", "t").DenyInput("team/svc", 42, jenkins.PendingInput{ID: "Promote"}))
	assert.Equal(t, "abc123", gotCrumb)
}
