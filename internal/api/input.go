package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
)

// pendingInputTree reads the run's InputAction through the classic /api/json
// exported-bean tree. InputAction is @ExportedBean with @Exported
// waitingForInput and executions, and InputStep exports message, ok, submitter,
// submitterParameter and parameters — so this one request carries everything a
// decision needs, including the submitter restriction. `tree` uses a
// name-driven pruner rather than the depth-based one, so the nested beans'
// @ExportedBean(defaultVisibility = 2) does not limit how deep it reaches.
//
// _class is requested explicitly because it is the only field whose absence is
// meaningful: Jenkins answers a tree query naming fields that do not exist with
// HTTP 200 and silently drops them, so no other field can distinguish "not
// pending" from "wrong field name". Actions with no exported properties render
// as {} — an InputAction never does.
const pendingInputTree = "actions[_class,waitingForInput," +
	"executions[id,settled,input[message,ok,submitter,submitterParameter," +
	"parameters[_class,name,type,description,defaultParameterValue[value],choices]]]]"

type inputActionPayload struct {
	Actions []struct {
		Class           string `json:"_class"`
		WaitingForInput bool   `json:"waitingForInput"`
		Executions      []struct {
			ID      string `json:"id"`
			Settled bool   `json:"settled"`
			Input   struct {
				Message            string                        `json:"message"`
				OK                 string                        `json:"ok"`
				Submitter          string                        `json:"submitter"`
				SubmitterParameter string                        `json:"submitterParameter"`
				Parameters         []jenkins.ParameterDefinition `json:"parameters"`
			} `json:"input"`
		} `json:"executions"`
	} `json:"actions"`
}

// GetInputState reports what a build's InputAction says about pending input
// steps. A build that never reached an input step carries no InputAction, which
// is reported as ActionPresent false — that is "nothing pending", not "plugin
// missing", and callers must not confuse the two.
func (c *Client) GetInputState(jobPath string, number int) (*jenkins.InputState, error) {
	path := fmt.Sprintf("%s/%d/api/json", NormalizeJobPath(jobPath), number)
	resp, err := c.Get(path, url.Values{"tree": {pendingInputTree}})
	if err != nil {
		if e := c.enrichNotFound(jobPath, err); e != err {
			return nil, e
		}
		return nil, fmt.Errorf("getting input steps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload inputActionPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding input steps: %w", err)
	}

	state := &jenkins.InputState{}
	for _, a := range payload.Actions {
		if !strings.Contains(a.Class, "InputAction") {
			continue
		}
		state.ActionPresent = true
		if a.WaitingForInput {
			state.WaitingForInput = true
		}
		for _, e := range a.Executions {
			if e.Settled {
				continue
			}
			state.Pending = append(state.Pending, jenkins.PendingInput{
				ID:                 e.ID,
				Message:            e.Input.Message,
				OK:                 e.Input.OK,
				Submitter:          e.Input.Submitter,
				SubmitterParameter: e.Input.SubmitterParameter,
				Parameters:         e.Input.Parameters,
			})
		}
	}
	return state, nil
}

// GetPendingInputs returns only the input steps awaiting a decision. An empty
// slice means nothing is pending.
func (c *Client) GetPendingInputs(jobPath string, number int) ([]jenkins.PendingInput, error) {
	state, err := c.GetInputState(jobPath, number)
	if err != nil {
		return nil, err
	}
	return state.Pending, nil
}

// ApproveInput settles an input step with "proceed".
//
// With no parameters it posts to proceedEmpty. That endpoint approves without
// collecting any values: doProceedEmpty calls proceed(handleSubmitterParameter()),
// which carries only the submitter id when the step asks for one and never the
// declared parameters or their defaults. So a step declaring ENV=staging|prod
// approves with ENV unset, not with staging. Callers must never reach it for a
// step with parameters, and ApproveInput enforces that here rather than
// trusting each call site.
func (c *Client) ApproveInput(jobPath string, number int, in jenkins.PendingInput, params map[string]string) error {
	if len(params) == 0 && len(in.Parameters) > 0 {
		return fmt.Errorf("refusing to approve input %q with no values: it declares %d parameter(s) (%s) and the parameter-less endpoint submits none of them, so the pipeline would receive them unset rather than defaulted",
			in.ID, len(in.Parameters), strings.Join(in.ParameterNames(), ", "))
	}
	for name := range params {
		if _, ok := in.Parameter(name); !ok {
			return fmt.Errorf("input %q declares no parameter %q — Jenkins would reject the submission; declared: %s",
				in.ID, name, strings.Join(in.ParameterNames(), ", "))
		}
	}

	base := fmt.Sprintf("%s/%d/input/%s", NormalizeJobPath(jobPath), number, url.PathEscape(in.ID))
	if len(params) == 0 {
		return c.postInput(base+"/proceedEmpty", nil, "approve", jobPath, number, in)
	}

	// /proceed reads the standard Stapler submitted form, so values travel in a
	// `json` form field shaped like the browser's input form. /submit is not used:
	// it ABORTS the input whenever the request carries no `proceed` field, which
	// turns a malformed approval into a denial.
	type field struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	fields := make([]field, 0, len(params))
	for _, d := range in.Parameters {
		if v, ok := params[d.Name]; ok {
			fields = append(fields, field{Name: d.Name, Value: v})
		}
	}
	encoded, err := json.Marshal(map[string]any{"parameter": fields})
	if err != nil {
		return fmt.Errorf("encoding input parameters: %w", err)
	}
	form := url.Values{"json": {string(encoded)}}
	return c.postInput(base+"/proceed", strings.NewReader(form.Encode()), "approve", jobPath, number, in)
}

// DenyInput settles an input step with "abort", which fails the input step and
// normally aborts the build.
func (c *Client) DenyInput(jobPath string, number int, in jenkins.PendingInput) error {
	path := fmt.Sprintf("%s/%d/input/%s/abort", NormalizeJobPath(jobPath), number, url.PathEscape(in.ID))
	return c.postInput(path, nil, "deny", jobPath, number, in)
}

func (c *Client) postInput(path string, body io.Reader, action, jobPath string, number int, in jenkins.PendingInput) error {
	contentType := ""
	if body != nil {
		contentType = "application/x-www-form-urlencoded"
	}
	resp, err := c.Post(path, body, contentType)
	if err != nil {
		return classifyInputFailure(err, action, jobPath, number, in, c.host)
	}
	CloseBody(resp)
	return nil
}

// classifyInputFailure turns Jenkins' response into a named cause. The
// submitter restriction is checked first because the step's own submitter list
// is evaluated before any permission and cannot be overridden by one, so when a
// step names submitters that is the refusal the user has to act on. Jenkins
// reports it through hudson.model.Failure, an HTML error page whose status and
// wording are not a stable contract, so the classification uses the submitter
// value jkit already read rather than parsing that page.
func classifyInputFailure(err error, action, jobPath string, number int, in jenkins.PendingInput, host string) error {
	base := &jenkins.InputDecisionError{
		Action:  action,
		InputID: in.ID,
		JobPath: jobPath,
		Build:   number,
		Host:    host,
		Cause:   err,
	}

	var nfe *jenkins.NotFoundError
	if errors.As(err, &nfe) {
		base.Reason = jenkins.InputRefusedGone
		return base
	}

	var permErr *jenkins.PermissionError
	isRefusal := errors.As(err, &permErr)
	var srvErr *jenkins.ServerError
	if errors.As(err, &srvErr) && srvErr.StatusCode < 500 {
		isRefusal = true
	}
	if !isRefusal {
		return err
	}

	if subs := in.Submitters(); len(subs) > 0 {
		base.Reason = jenkins.InputRefusedSubmitter
		base.Submitter = strings.Join(subs, ", ")
		return base
	}
	base.Reason = jenkins.InputRefusedPermission
	return base
}
