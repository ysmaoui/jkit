# Quickstart

## Install

From source (requires Go 1.24+):

```bash
git clone https://github.com/ysmaoui/jk && cd jk
go build -o jk .
sudo mv jk /usr/local/bin/
```

Pre-built binaries available via [GitHub Releases](https://github.com/ysmaoui/jk/releases) for Linux, macOS, and Windows (amd64/arm64).

## Authenticate

Interactive:

```bash
jk auth login
# Jenkins host URL: https://jenkins.company.com
# Username: jane
# API token: ****
# ✓ Logged in to https://jenkins.company.com as jane
```

Non-interactive (CI/scripts):

```bash
jk auth login --host https://jenkins.company.com --user jane --token $JENKINS_TOKEN
```

Verify:

```bash
jk auth status
# Host:  https://jenkins.company.com
# User:  jane
# Auth:  valid
```

## First Commands

```bash
jk list                        # list all jobs
jk run my-app                  # trigger a build
jk run my-app --wait           # trigger and wait for result
jk status my-app               # recent build history
jk status my-app 42            # build #42 detail + stages
jk log my-app -f               # stream live build log
jk log my-app --grep ERROR     # filter log lines
jk test my-app --failed        # show failed tests
jk abort my-app                # abort running build
jk diagnose my-app             # analyze failed build
jk open my-app                 # open in browser
jk lint                        # validate ./Jenkinsfile
```

## Zero-Config Job Detection

Commands like `run`, `status`, `log`, and `open` auto-detect the job name when no argument is given. Detection order:

1. **`.jk.yml`** in current or parent directory
2. **Git remote** — extracts `org/repo` from `origin`
3. **Directory name** — uses basename of cwd

Create `.jk.yml` for explicit control:

```yaml
job: team/backend/my-service
host: https://jenkins.company.com  # optional
```

Then just:

```bash
jk run          # runs team/backend/my-service
jk status       # shows its builds
```

## Shell Completions

```bash
# Bash
jk completion bash >> ~/.bashrc

# Zsh
jk completion zsh >> ~/.zshrc

# Fish
jk completion fish > ~/.config/fish/completions/jk.fish

# PowerShell
jk completion powershell >> $PROFILE
```
