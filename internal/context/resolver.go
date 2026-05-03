package context

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type ResolvedContext struct {
	JobPath string
	Host    string
	Source  string // "jk.yml", "git-remote", "dirname"
}

type jkConfig struct {
	Job  string `yaml:"job"`
	Host string `yaml:"host,omitempty"`
}

func Resolve() (*ResolvedContext, error) {
	// 1. Check .jk.yml
	if ctx, err := resolveFromJKYml(); err == nil && ctx != nil {
		return ctx, nil
	}

	// 2. Git remote
	if ctx, err := resolveFromGitRemote(); err == nil && ctx != nil {
		return ctx, nil
	}

	// 3. Directory name
	if ctx, err := resolveFromDirname(); err == nil && ctx != nil {
		return ctx, nil
	}

	return nil, fmt.Errorf("could not detect Jenkins job — specify job arg or create .jk.yml")
}

func resolveFromJKYml() (*ResolvedContext, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	for {
		path := filepath.Join(dir, ".jk.yml")
		data, err := os.ReadFile(path)
		if err == nil {
			var cfg jkConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return nil, err
			}
			if cfg.Job != "" {
				return &ResolvedContext{JobPath: cfg.Job, Host: cfg.Host, Source: "jk.yml"}, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, fmt.Errorf(".jk.yml not found")
}

// ParseGitRemote extracts org/repo from a git remote URL string.
// Supports SCP-style (git@host:org/repo) and URL-style (https://, ssh://, git://).
func ParseGitRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	var orgRepo string

	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		// SCP-style: git@host:org/repo
		if idx := strings.Index(remote, ":"); idx >= 0 {
			orgRepo = remote[idx+1:]
		}
	} else {
		// URL-style (https://, ssh://, git://)
		u, err := url.Parse(remote)
		if err == nil && u.Path != "" {
			p := strings.TrimPrefix(u.Path, "/")
			parts := strings.Split(p, "/")
			if len(parts) >= 2 {
				orgRepo = parts[len(parts)-2] + "/" + parts[len(parts)-1]
			}
		}
	}

	if orgRepo == "" {
		return "", fmt.Errorf("could not parse git remote: %s", remote)
	}

	return orgRepo, nil
}

func resolveFromGitRemote() (*ResolvedContext, error) {
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return nil, err
	}

	orgRepo, err := ParseGitRemote(string(out))
	if err != nil {
		return nil, err
	}

	return &ResolvedContext{JobPath: orgRepo, Source: "git-remote"}, nil
}

func resolveFromDirname() (*ResolvedContext, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	name := filepath.Base(dir)
	if name == "" || name == "." || name == "/" {
		return nil, fmt.Errorf("invalid directory")
	}
	return &ResolvedContext{JobPath: name, Source: "dirname"}, nil
}
