package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Hosts map[string]*HostConfig `yaml:"hosts"`
}

type HostConfig struct {
	User    string `yaml:"user"`
	Token   string `yaml:"token"`
	Default bool   `yaml:"default,omitempty"`
	Alias   string `yaml:"alias,omitempty"`
}

// ResolveHost resolves a host URL or alias to the actual host URL and its config.
// Checks exact match first, then alias match.
func (c *Config) ResolveHost(hostOrAlias string) (string, *HostConfig, error) {
	// Exact match
	if hc, ok := c.Hosts[hostOrAlias]; ok {
		return hostOrAlias, hc, nil
	}
	// Alias match
	for host, hc := range c.Hosts {
		if hc.Alias != "" && hc.Alias == hostOrAlias {
			return host, hc, nil
		}
	}
	return "", nil, fmt.Errorf("host %q not configured — run 'jkit auth login --host %s'", hostOrAlias, hostOrAlias)
}

func Load() (*Config, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{Hosts: make(map[string]*HostConfig)}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]*HostConfig)
	}
	return &cfg, nil
}

func (c *Config) Save() error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	path := filepath.Join(dir, "config.yml")
	return os.WriteFile(path, data, 0600)
}

func (c *Config) DefaultHost() (string, *HostConfig, error) {
	if len(c.Hosts) == 0 {
		return "", nil, errors.New("no hosts configured — run 'jkit auth login'")
	}
	if len(c.Hosts) == 1 {
		for host, hc := range c.Hosts {
			return host, hc, nil
		}
	}
	for host, hc := range c.Hosts {
		if hc.Default {
			return host, hc, nil
		}
	}
	return "", nil, errors.New("multiple hosts configured, none marked default — run 'jkit auth login --host HOST'")
}

func ConfigDir() (string, error) {
	if d := os.Getenv("JKIT_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "jkit"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, ".config", "jkit"), nil
}
