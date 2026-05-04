package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
)

// hostFromCmd loads config, resolves --host override, returns the host URL without creating an API client.
func hostFromCmd(cmd *cobra.Command) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	host, _, err := cfg.DefaultHost()
	if err != nil {
		return "", err
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		resolved, _, err := cfg.ResolveHost(h)
		if err != nil {
			return "", err
		}
		host = resolved
	}
	return host, nil
}

// clientFromCmd loads config, resolves --host override, returns an API client and host URL.
func clientFromCmd(cmd *cobra.Command) (*api.Client, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	host, hc, err := cfg.DefaultHost()
	if err != nil {
		return nil, "", err
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		resolved, rc, err := cfg.ResolveHost(h)
		if err != nil {
			return nil, "", err
		}
		host = resolved
		hc = rc
	}
	opts := clientOpts(cmd)
	return api.NewClient(host, hc.User, hc.Token, opts...), host, nil
}

// clientFromURL creates an API client by looking up credentials for the given host URL.
// The host is matched by scheme+host+port against configured hosts.
func clientFromURL(cfg *config.Config, host string, opts ...api.ClientOption) (*api.Client, error) {
	host = strings.TrimRight(host, "/")

	// Exact match
	if hc, ok := cfg.Hosts[host]; ok {
		return api.NewClient(host, hc.User, hc.Token, opts...), nil
	}

	// Normalized match (strip trailing slash from config keys)
	for cfgHost, hc := range cfg.Hosts {
		if strings.TrimRight(cfgHost, "/") == host {
			return api.NewClient(host, hc.User, hc.Token, opts...), nil
		}
	}

	return nil, fmt.Errorf("no credentials for %q — run 'jkit auth login --host %s'", host, host)
}

func clientOpts(cmd *cobra.Command) []api.ClientOption {
	var opts []api.ClientOption
	if v, _ := cmd.Flags().GetBool("verbose"); v {
		opts = append(opts, api.WithVerbose())
	}
	if t, _ := cmd.Flags().GetString("timeout"); t != "" && t != "30s" {
		if d, err := time.ParseDuration(t); err == nil {
			opts = append(opts, api.WithTimeout(d))
		}
	}
	if s, _ := cmd.Flags().GetString("pipeline-source"); s != "" {
		opts = append(opts, api.WithPipelineSource(parsePipelineSourceFlag(s)))
	}
	return opts
}

func parsePipelineSourceFlag(s string) api.PipelineSource {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pgv", "pipeline-graph-view":
		return api.PipelineSourcePGV
	case "blueocean", "blue-ocean", "blue":
		return api.PipelineSourceBlueOcean
	default:
		return api.PipelineSourceAuto
	}
}
