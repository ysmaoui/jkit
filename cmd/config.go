package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/config"
	"github.com/ysmaoui/jk/internal/output"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all configured hosts",
	Args:  cobra.NoArgs,
	RunE:  runConfigList,
}

var configSetDefaultCmd = &cobra.Command{
	Use:   "set-default <host-or-alias>",
	Short: "Set the default Jenkins host",
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigSetDefault,
}

var configRemoveCmd = &cobra.Command{
	Use:   "remove <host-or-alias>",
	Short: "Remove a configured host",
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigRemove,
}

var configSetAliasCmd = &cobra.Command{
	Use:   "set-alias <host> <alias>",
	Short: "Set or change alias for a host",
	Args:  cobra.ExactArgs(2),
	RunE:  runConfigSetAlias,
}

func init() {
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configSetDefaultCmd)
	configCmd.AddCommand(configRemoveCmd)
	configCmd.AddCommand(configSetAliasCmd)
	rootCmd.AddCommand(configCmd)
}

type hostRow struct {
	Host    string
	User    string
	Alias   string
	Default bool
}

func runConfigList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(cfg.Hosts) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No hosts configured — run 'jk auth login'")
		return nil
	}

	isJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")
	f := output.NewFormatter(os.Stdout, isJSON, tmpl)

	var rows []any
	for host, hc := range cfg.Hosts {
		rows = append(rows, hostRow{
			Host:    host,
			User:    hc.User,
			Alias:   hc.Alias,
			Default: hc.Default,
		})
	}

	if isJSON || tmpl != "" {
		return f.Output(rows, nil)
	}

	columns := []output.Column{
		{Header: "HOST", Field: func(v any) string { return v.(hostRow).Host }},
		{Header: "USER", Field: func(v any) string { return v.(hostRow).User }},
		{Header: "ALIAS", Field: func(v any) string {
			a := v.(hostRow).Alias
			if a == "" {
				return "-"
			}
			return a
		}},
		{Header: "DEFAULT", Field: func(v any) string {
			if v.(hostRow).Default {
				return "yes"
			}
			return ""
		}},
	}

	return f.Output(rows, columns)
}

func runConfigSetDefault(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	targetHost, _, err := cfg.ResolveHost(args[0])
	if err != nil {
		return err
	}

	for host, hc := range cfg.Hosts {
		hc.Default = (host == targetHost)
	}

	if err := cfg.Save(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stderr, "Default host set to %s\n", targetHost)
	return nil
}

func runConfigRemove(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	targetHost, _, err := cfg.ResolveHost(args[0])
	if err != nil {
		return err
	}

	delete(cfg.Hosts, targetHost)

	if err := cfg.Save(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stderr, "Removed host %s\n", targetHost)
	return nil
}

func runConfigSetAlias(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	host := args[0]
	alias := args[1]

	hc, ok := cfg.Hosts[host]
	if !ok {
		return fmt.Errorf("host %q not configured", host)
	}

	hc.Alias = alias

	if err := cfg.Save(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stderr, "Alias for %s set to %q\n", host, alias)
	return nil
}
