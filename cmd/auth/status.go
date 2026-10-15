package auth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/authcheck"
	"github.com/ysmaoui/jkit/internal/config"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	RunE:  runStatus,
}

func init() {
	statusCmd.Flags().Bool("all", false, "Check every configured host and flag malformed, duplicate or ambiguous keys")
	AuthCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if all, _ := cmd.Flags().GetBool("all"); all {
		if h, _ := cmd.Flags().GetString("host"); h != "" {
			return fmt.Errorf("--all and --host cannot be used together")
		}
		return runStatusAll(cmd, cfg)
	}

	var host string
	var hc *config.HostConfig
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		host, hc, err = cfg.ResolveHost(h)
	} else {
		host, hc, err = cfg.DefaultHost()
	}
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stdout, "Host:  %s\n", host)
	_, _ = fmt.Fprintf(os.Stdout, "User:  %s\n", hc.User)

	client := api.NewClient(host, hc.User, hc.Token)
	resp, err := client.Get("/api/json", nil)
	if err == nil {
		api.CloseBody(resp)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "Auth:  invalid (%s)\n", err)
		return &jenkins.ExitError{Code: 1, Message: "auth invalid"}
	}
	_, _ = fmt.Fprintf(os.Stdout, "Auth:  valid\n")
	return nil
}

func runStatusAll(cmd *cobra.Command, cfg *config.Config) error {
	if len(cfg.Hosts) == 0 {
		return fmt.Errorf("no hosts configured — run 'jkit auth login'")
	}
	_, _ = fmt.Fprintf(os.Stderr, "Checking %d host(s)...\n", len(cfg.Hosts))
	entries := authcheck.CheckAll(context.Background(), cfg, authcheck.Options{})

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)
	var outErr error
	if isJSON || tmpl != "" {
		outErr = f.Output(entries, nil)
	} else {
		rows := make([]any, len(entries))
		for i, e := range entries {
			rows[i] = e
		}
		outErr = f.Output(rows, statusColumns)
	}
	if outErr != nil {
		return outErr
	}

	var failed, unverified int
	for _, e := range entries {
		if e.Failed() {
			failed++
		} else if e.Unverified() {
			unverified++
		}
	}
	if unverified > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "warning: %d host(s) could not be checked\n", unverified)
	}
	if failed > 0 {
		return &jenkins.ExitError{Code: 1, Message: fmt.Sprintf("%d host(s) have rejected or unproven credentials or key problems", failed)}
	}
	return nil
}

var statusColumns = []output.Column{
	{Header: "HOST", Field: func(v any) string { return v.(authcheck.Entry).Host }},
	{Header: "ALIAS", Field: func(v any) string { return dash(v.(authcheck.Entry).Alias) }},
	{Header: "USER", Field: func(v any) string { return dash(v.(authcheck.Entry).User) }},
	{Header: "DEFAULT", Field: func(v any) string {
		if v.(authcheck.Entry).Default {
			return "yes"
		}
		return ""
	}},
	{Header: "RESULT", Field: func(v any) string {
		e := v.(authcheck.Entry)
		bad := e.Result == authcheck.ResultInvalid || e.Result == authcheck.ResultForbidden || e.Result == authcheck.ResultAnonymous
		return output.ColorAuthResult(resultLabel(e), e.Result == authcheck.ResultValid, bad)
	}},
	{Header: "NOTES", Field: func(v any) string { return strings.Join(v.(authcheck.Entry).Problems, "; ") }},
}

func resultLabel(e authcheck.Entry) string {
	switch e.Result {
	case authcheck.ResultValid, authcheck.ResultUnreachable, authcheck.ResultTimeout, authcheck.ResultAnonymous:
		return e.Result
	case authcheck.ResultInvalid:
		return "invalid token (401)"
	case authcheck.ResultForbidden:
		return "forbidden (403)"
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
