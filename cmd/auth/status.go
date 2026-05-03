package auth

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/api"
	"github.com/ysmaoui/jk/internal/config"
	"github.com/ysmaoui/jk/internal/jenkins"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	RunE:  runStatus,
}

func init() {
	AuthCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	host, hc, err := cfg.DefaultHost()
	if err != nil {
		return err
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		host = h
		if c, ok := cfg.Hosts[h]; ok {
			hc = c
		} else {
			return fmt.Errorf("host %q not configured — run 'jk auth login --host %s'", h, h)
		}
	}

	fmt.Fprintf(os.Stdout, "Host:  %s\n", host)
	fmt.Fprintf(os.Stdout, "User:  %s\n", hc.User)

	client := api.NewClient(host, hc.User, hc.Token)
	resp, err := client.Get("/api/json", nil)
	if err == nil {
		api.CloseBody(resp)
	}
	if err != nil {
		fmt.Fprintf(os.Stdout, "Auth:  invalid (%s)\n", err)
		return &jenkins.ExitError{Code: 1, Message: "auth invalid"}
	}
	fmt.Fprintf(os.Stdout, "Auth:  valid\n")
	return nil
}
