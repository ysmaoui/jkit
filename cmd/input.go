package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var inputCmd = &cobra.Command{
	Use:   "input [job] [build#]",
	Short: "List, approve or deny a build's pending input steps",
	Long: `Show the 'input' steps a running build is paused on, and answer them without
opening Jenkins. With no mode flag the pending steps are listed; --approve and
--deny settle one of them.

Approving a step that declares parameters requires passing every one with
--param. Jenkins' parameter-less approve endpoint would otherwise submit the
declared defaults, which on a promote-to-prod gate deploys the wrong values.`,
	Example: `  jkit input my-app
  jkit input my-app 42
  jkit input my-app 42 --approve
  jkit input my-app 42 --approve --id Deploy -p VERSION=1.4.2
  jkit input my-app 42 --deny`,
	Args: cobra.MaximumNArgs(2),
	RunE: runInput,
}

// registerInputFlags is called from init and from the test harness, which resets
// every subcommand's flags between runs.
func registerInputFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("approve", false, "Approve (proceed) a pending input step")
	cmd.Flags().Bool("deny", false, "Deny (abort) a pending input step")
	cmd.Flags().String("id", "", "Input step ID to act on, from the ID column — not a stage node ID")
	cmd.Flags().StringArrayP("param", "p", nil, "Value for a parameter the input step declares (KEY=VALUE)")
}

func init() {
	registerInputFlags(inputCmd)
	rootCmd.AddCommand(inputCmd)
}

func runInput(cmd *cobra.Command, args []string) error {
	client, jobPath, buildNum, err := resolveJobArgs(cmd, args, true)
	if err != nil {
		return err
	}
	if buildNum <= 0 {
		job, err := client.GetJob(jobPath)
		if err != nil {
			return err
		}
		if job.LastBuild == nil {
			return fmt.Errorf("no builds found for %s", jobPath)
		}
		buildNum = job.LastBuild.Number
	}

	approve, _ := cmd.Flags().GetBool("approve")
	deny, _ := cmd.Flags().GetBool("deny")
	if approve && deny {
		return fmt.Errorf("cannot use --approve and --deny together")
	}

	state, err := client.GetInputState(jobPath, buildNum)
	if err != nil {
		return err
	}

	if !approve && !deny {
		return listInputs(cmd, jobPath, buildNum, state)
	}
	return decideInput(cmd, client, jobPath, buildNum, state, approve)
}

func listInputs(cmd *cobra.Command, jobPath string, buildNum int, state *jenkins.InputState) error {
	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	if isJSON || tmpl != "" {
		// Always a list, never null: a build with nothing pending is the common
		// case and a script filtering the output should not have to special-case it.
		pending := state.Pending
		if pending == nil {
			pending = []jenkins.PendingInput{}
		}
		return f.Output(pending, nil)
	}
	if len(state.Pending) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "No input step waiting on %s #%d — %s.\n", jobPath, buildNum, noPendingReason(state))
		return nil
	}

	rows := make([]any, len(state.Pending))
	for i := range state.Pending {
		rows[i] = state.Pending[i]
	}
	columns := []output.Column{
		{Header: "ID", Field: func(v any) string { return v.(jenkins.PendingInput).ID }},
		{Header: "MESSAGE", Field: func(v any) string {
			return truncate(collapseWS(v.(jenkins.PendingInput).Message), 60)
		}},
		{Header: "PARAMETERS", Field: func(v any) string {
			names := v.(jenkins.PendingInput).ParameterNames()
			if len(names) == 0 {
				return "-"
			}
			return strings.Join(names, ", ")
		}},
		{Header: "SUBMITTER", Field: func(v any) string {
			subs := v.(jenkins.PendingInput).Submitters()
			if len(subs) == 0 {
				return "anyone with Job/Build"
			}
			return strings.Join(subs, ", ")
		}},
	}
	return f.Output(rows, columns)
}

// noPendingReason explains an empty list. Nothing pending is the ordinary answer
// for most builds and never evidence that a plugin is absent, so the wording
// says which of the two ordinary states the build is in and stops there.
func noPendingReason(state *jenkins.InputState) string {
	if state.ActionPresent {
		return "its input steps have all been answered"
	}
	return "the build never reached one"
}

func decideInput(cmd *cobra.Command, client *api.Client, jobPath string, buildNum int, state *jenkins.InputState, approve bool) error {
	action := "deny"
	if approve {
		action = "approve"
	}
	if len(state.Pending) == 0 {
		return fmt.Errorf("nothing to %s on %s #%d — %s", action, jobPath, buildNum, noPendingReason(state))
	}

	wantID, _ := cmd.Flags().GetString("id")
	target, err := selectInput(state.Pending, wantID, jobPath, buildNum)
	if err != nil {
		return err
	}

	rawParams, _ := cmd.Flags().GetStringArray("param")
	params, err := parseInputParams(rawParams)
	if err != nil {
		return err
	}

	if !approve {
		if len(params) > 0 {
			return fmt.Errorf("--param applies only to --approve; denying an input submits no values")
		}
		if err := client.DenyInput(jobPath, buildNum, *target); err != nil {
			return err
		}
		return reportDecision(cmd, "deny", jobPath, buildNum, *target, nil)
	}

	if err := checkApproveParams(*target, params, jobPath, buildNum); err != nil {
		return err
	}
	if err := client.ApproveInput(jobPath, buildNum, *target, params); err != nil {
		return err
	}
	return reportDecision(cmd, "approve", jobPath, buildNum, *target, params)
}

// selectInput picks the input to act on. Acting without --id is allowed only
// when there is exactly one candidate, so a build paused on two gates can never
// have the wrong one settled by omission.
func selectInput(pending []jenkins.PendingInput, wantID, jobPath string, buildNum int) (*jenkins.PendingInput, error) {
	ids := make([]string, len(pending))
	for i, p := range pending {
		ids[i] = p.ID
	}
	if wantID == "" {
		if len(pending) == 1 {
			return &pending[0], nil
		}
		return nil, fmt.Errorf("%s #%d has %d pending input steps — name one with --id: %s",
			jobPath, buildNum, len(pending), strings.Join(ids, ", "))
	}
	for i := range pending {
		if pending[i].ID == wantID {
			return &pending[i], nil
		}
	}
	msg := fmt.Sprintf("no pending input step %q on %s #%d — pending: %s",
		wantID, jobPath, buildNum, strings.Join(ids, ", "))
	if isAllDigits(wantID) {
		msg += "\n--id takes the input step's ID, which is not a pipeline stage node ID; 'jkit stages' IDs do not belong here"
	}
	return nil, fmt.Errorf("%s", msg)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func parseInputParams(raw []string) (map[string]string, error) {
	params := make(map[string]string, len(raw))
	for _, p := range raw {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid parameter format %q — use KEY=VALUE", p)
		}
		params[k] = v
	}
	return params, nil
}

// checkApproveParams refuses an approval that would let Jenkins choose values.
// The parameter-less endpoint approves a step that declares parameters using
// their defaults, so every declared parameter has to arrive explicitly; the
// error prints the ready-made command rather than only naming the problem.
func checkApproveParams(in jenkins.PendingInput, params map[string]string, jobPath string, buildNum int) error {
	for name := range params {
		if _, ok := in.Parameter(name); !ok {
			return fmt.Errorf("input %q declares no parameter %q — declared: %s",
				in.ID, name, strings.Join(in.ParameterNames(), ", "))
		}
	}
	var missing []string
	for _, name := range in.ParameterNames() {
		if _, ok := params[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "refusing to approve input %q on %s #%d: it declares %d parameter(s) and %d of them (%s) have no value.\n",
		in.ID, jobPath, buildNum, len(in.Parameters), len(missing), strings.Join(missing, ", "))
	b.WriteString("Approving without them would submit the defaults Jenkins holds, which is not the same as agreeing to them.\n")
	b.WriteString("Declared parameters:\n")
	for _, d := range in.Parameters {
		def := d.DefaultString()
		if def == "" {
			def = "(none)"
		}
		fmt.Fprintf(&b, "  %-24s %-10s default: %s", d.Name, d.Kind(), collapseWS(def))
		if choices := d.Choices(); len(choices) > 0 {
			fmt.Fprintf(&b, "   choices: %s", strings.Join(choices, ", "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Pass every value explicitly:\n  jkit input %s %d --approve --id %s", jobPath, buildNum, in.ID)
	for _, d := range in.Parameters {
		v := params[d.Name]
		if v == "" {
			v = "<value>"
		}
		fmt.Fprintf(&b, " -p %s=%s", d.Name, v)
	}
	return fmt.Errorf("%s", b.String())
}

func reportDecision(cmd *cobra.Command, action, jobPath string, buildNum int, in jenkins.PendingInput, params map[string]string) error {
	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	if isJSON || tmpl != "" {
		result := struct {
			Action     string            `json:"action"`
			Job        string            `json:"job"`
			Build      int               `json:"build"`
			ID         string            `json:"id"`
			Message    string            `json:"message"`
			Parameters map[string]string `json:"parameters,omitempty"`
		}{action, jobPath, buildNum, in.ID, in.Message, params}
		return output.NewFormatter(os.Stdout, isJSON, tmpl).Output(result, nil)
	}

	verb := "Denied"
	if action == "approve" {
		verb = "Approved"
	}
	msg := fmt.Sprintf("%s input %q on %s #%d", verb, in.ID, jobPath, buildNum)
	if in.Message != "" {
		msg += fmt.Sprintf(" — %s", collapseWS(in.Message))
	}
	_, _ = fmt.Fprintln(os.Stderr, msg)
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(os.Stderr, "  %s=%s\n", k, params[k])
		}
	}
	if action == "deny" {
		_, _ = fmt.Fprintln(os.Stderr, "The build fails the input step and normally aborts from here.")
	}
	return nil
}
