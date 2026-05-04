package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a Jenkins host",
	RunE:  runLogin,
}

func init() {
	loginCmd.Flags().String("user", "", "Username")
	loginCmd.Flags().String("token", "", "API token")
	loginCmd.Flags().String("alias", "", "Short alias for this host (e.g., prod, staging)")
	AuthCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, args []string) error {
	host, _ := cmd.Flags().GetString("host")
	user, _ := cmd.Flags().GetString("user")
	token, _ := cmd.Flags().GetString("token")

	reader := bufio.NewReader(os.Stdin)

	if host == "" {
		fmt.Print("Jenkins host URL: ")
		h, _ := reader.ReadString('\n')
		host = strings.TrimSpace(h)
	}
	host = strings.TrimRight(host, "/")
	if host == "" {
		return fmt.Errorf("host is required")
	}

	if user == "" {
		fmt.Print("Username: ")
		u, _ := reader.ReadString('\n')
		user = strings.TrimSpace(u)
	}
	if user == "" {
		return fmt.Errorf("username is required")
	}

	if token == "" {
		fmt.Print("API token: ")
		raw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("reading token: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}

	// Validate credentials
	client := api.NewClient(host, user, token)
	resp, err := client.Get("/api/json", nil)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	api.CloseBody(resp)

	// Save to config
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	alias, _ := cmd.Flags().GetString("alias")
	existing, exists := cfg.Hosts[host]
	isDefault := len(cfg.Hosts) == 0 || (exists && existing.Default)
	cfg.Hosts[host] = &config.HostConfig{
		User:    user,
		Token:   token,
		Default: isDefault,
		Alias:   alias,
	}

	if err := cfg.Save(); err != nil {
		return err
	}

	cfgDir, _ := config.ConfigDir()
	_, _ = fmt.Fprintf(os.Stderr, "✓ Logged in to %s as %s\n", host, user)
	_, _ = fmt.Fprintf(os.Stderr, "  Token stored in %s\n", cfgDir)
	return nil
}
