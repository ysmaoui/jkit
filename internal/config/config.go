package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// ResolveHost resolves a host URL or alias to the configured host key and its
// config. Tries an exact key, then a key equal after NormalizeHost on both
// sides, then an alias. Several keys normalising to the input is an error,
// since callers such as config remove act on the key returned. Keys are
// visited in sorted order so a duplicate alias resolves the same way every run.
func (c *Config) ResolveHost(hostOrAlias string) (string, *HostConfig, error) {
	if hc, ok := c.Hosts[hostOrAlias]; ok {
		return hostOrAlias, hc, nil
	}
	if want := NormalizeHost(hostOrAlias); want != "" {
		var matches []string
		for _, host := range c.HostURLs() {
			if NormalizeHost(host) == want {
				matches = append(matches, host)
			}
		}
		switch len(matches) {
		case 0:
		case 1:
			return matches[0], c.Hosts[matches[0]], nil
		default:
			return "", nil, fmt.Errorf("host %q matches several configured keys, give one exactly: %s", hostOrAlias, strings.Join(matches, ", "))
		}
	}
	for _, host := range c.HostURLs() {
		if hc := c.Hosts[host]; hc.Alias != "" && hc.Alias == hostOrAlias {
			return host, hc, nil
		}
	}
	return "", nil, fmt.Errorf("host %q not configured — run 'jkit auth login --host %s'", hostOrAlias, hostOrAlias)
}

// HostURLs returns the configured host keys, each a Jenkins root URL.
func (c *Config) HostURLs() []string {
	hosts := make([]string, 0, len(c.Hosts))
	for h := range c.Hosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// NormalizeHost trims whitespace, any fragment or query, and trailing slashes
// from a host URL. A fragment or query makes a second config key for the same
// Jenkins that url.Parse cannot tell apart from the first.
func NormalizeHost(host string) string {
	host, _, _ = strings.Cut(strings.TrimSpace(host), "#")
	host, _, _ = strings.Cut(host, "?")
	return strings.TrimRight(host, "/")
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
	return Parse(data)
}

// Parse reads config.yml content. Empty input is a config without hosts.
func Parse(data []byte) (*Config, error) {
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

// Path returns the location of config.yml.
func Path() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yml"), nil
}

// ReplaceWithBackup copies config.yml to config.yml.bak, then writes c over
// it, both 0600 and each through a rename so a crash leaves either the old or
// the new file. A symlinked config.yml stays a symlink: the new content goes
// to its target, the backup next to the link. It refuses when config.yml no
// longer holds expected, so a change made since it was read is not lost.
// Returns the backup path.
func (c *Config) ReplaceWithBackup(expected []byte) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading config: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return "", fmt.Errorf("%s changed while hosts were checked — re-run", path)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolving config path: %w", err)
	}
	backup := path + ".bak"
	if err := writeFileAtomic(backup, current); err != nil {
		return "", fmt.Errorf("writing backup: %w", err)
	}
	if err := writeFileAtomic(target, data); err != nil {
		return "", fmt.Errorf("writing config: %w", err)
	}
	return backup, nil
}

// writeFileAtomic replaces path with a fresh 0600 file, so an existing file's
// looser mode does not survive.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
