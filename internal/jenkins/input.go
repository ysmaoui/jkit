package jenkins

import (
	"fmt"
	"strings"
)

// PendingInput is one `input` step waiting for a decision on a running build.
type PendingInput struct {
	ID                 string                `json:"id"`
	Message            string                `json:"message"`
	OK                 string                `json:"ok,omitempty"`
	Submitter          string                `json:"submitter,omitempty"`
	SubmitterParameter string                `json:"submitterParameter,omitempty"`
	Parameters         []ParameterDefinition `json:"parameters,omitempty"`
}

// Submitters splits the step's submitter restriction into individual user or
// group names. Jenkins stores it as a comma-separated list and compares each
// entry trimmed, so the same trimming is applied here.
func (p PendingInput) Submitters() []string {
	if strings.TrimSpace(p.Submitter) == "" {
		return nil
	}
	parts := strings.Split(p.Submitter, ",")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParameterNames lists the parameters the step declares, in declaration order.
func (p PendingInput) ParameterNames() []string {
	names := make([]string, len(p.Parameters))
	for i, d := range p.Parameters {
		names[i] = d.Name
	}
	return names
}

// Parameter finds a declared parameter by exact name.
func (p PendingInput) Parameter(name string) (ParameterDefinition, bool) {
	for _, d := range p.Parameters {
		if d.Name == name {
			return d, true
		}
	}
	return ParameterDefinition{}, false
}

// InputState is what a build's InputAction says about pending decisions. The
// three cases are deliberately distinct: a run that never reached an input step
// has no InputAction at all, while a run that already answered one keeps the
// action with an empty execution list. Reporting both as "nothing pending" is
// correct but the reason differs, and neither means the plugin is absent.
type InputState struct {
	// ActionPresent is true when the run carries an InputAction, i.e. it reached
	// at least one input step during its life.
	ActionPresent bool
	// WaitingForInput mirrors InputAction.isWaitingForInput().
	WaitingForInput bool
	Pending         []PendingInput
}

// Input decision refusal causes. Approving or denying can fail for reasons that
// look alike over HTTP but need different fixes, so each is named separately.
const (
	// InputRefusedPermission — the token lacks Job/Build (approve) or
	// Job/Cancel (deny) on the job.
	InputRefusedPermission = "permission"
	// InputRefusedSubmitter — the step names who may answer it, and no Jenkins
	// permission overrides that list.
	InputRefusedSubmitter = "submitter"
	// InputRefusedGone — the input was answered by someone else, or the build
	// ended, between listing it and deciding it.
	InputRefusedGone = "gone"
)

// InputDecisionError explains a refused approve or deny. It always names one of
// the causes above, so a refusal is never ambiguous between "you may not",
// "someone else already did" and "the target vanished". A missing plugin is not
// among them: the endpoints only exist when pipeline-input-step is installed,
// and jkit listed the input through that same plugin moments earlier.
type InputDecisionError struct {
	Action    string // "approve" or "deny"
	InputID   string
	JobPath   string
	Build     int
	Host      string
	Reason    string
	Submitter string
	Cause     error
}

func (e *InputDecisionError) Error() string {
	target := fmt.Sprintf("input %q on %s #%d", e.InputID, e.JobPath, e.Build)
	switch e.Reason {
	case InputRefusedSubmitter:
		return fmt.Sprintf("cannot %s %s — the step restricts who may answer it to %s, and no Jenkins permission overrides that list (%s)",
			e.Action, target, e.Submitter, e.Host)
	case InputRefusedGone:
		return fmt.Sprintf("%s is no longer pending — it was answered elsewhere, or the build ended. Re-run 'jkit input %s %d' to see what is left",
			target, e.JobPath, e.Build)
	default:
		return fmt.Sprintf("cannot %s %s — %s is required on the job %q at %s",
			e.Action, target, e.permission(), e.JobPath, e.Host)
	}
}

func (e *InputDecisionError) permission() string {
	if e.Action == "deny" {
		return "Job/Cancel (or Job/Build)"
	}
	return "Job/Build"
}

func (e *InputDecisionError) Unwrap() error { return e.Cause }
