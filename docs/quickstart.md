# Quickstart

## Install

From source (requires Go 1.24+):

```bash
git clone https://github.com/ysmaoui/jkit && cd jkit
go build -o jkit .
sudo mv jkit /usr/local/bin/
```

Pre-built binaries available via [GitHub Releases](https://github.com/ysmaoui/jkit/releases) for Linux, macOS, and Windows (amd64/arm64).

## Authenticate

Interactive:

```bash
jkit auth login
# Jenkins host URL: https://jenkins.company.com
# Username: jane
# API token: ****
# ✓ Logged in to https://jenkins.company.com as jane
```

Non-interactive (CI/scripts):

```bash
jkit auth login --host https://jenkins.company.com --user jane --token $JENKINS_TOKEN
```

Verify:

```bash
jkit auth status
# Host:  https://jenkins.company.com
# User:  jane
# Auth:  valid
```

## First Commands

```bash
jkit list                        # list all jobs
jkit run my-app                  # trigger a build
jkit run my-app --wait           # trigger and wait for result
jkit status my-app               # recent build history
jkit status my-app 42            # build #42 detail + stages
jkit stages my-app 42            # stage IDs + qualified paths
jkit log my-app -f               # stream live build log
jkit log my-app --grep ERROR     # filter log lines
jkit log my-app --stage "Build"  # one stage's log
jkit test my-app --failed        # show failed tests
jkit abort my-app                # abort running build
jkit diagnose my-app             # analyze failed build
jkit open my-app                 # open in browser
jkit lint                        # validate ./Jenkinsfile
```

## Zero-Config Job Detection

Commands like `run`, `status`, `log`, and `open` auto-detect the job name when no argument is given. Detection order:

1. **`.jkit.yml`** in current or parent directory
2. **Git remote** — extracts `org/repo` from `origin`
3. **Directory name** — uses basename of cwd

Create `.jkit.yml` for explicit control:

```yaml
job: team/backend/my-service
host: https://jenkins.company.com  # optional
```

Then just:

```bash
jkit run          # runs team/backend/my-service
jkit status       # shows its builds
```

## Shell Completions

```bash
# Bash
jkit completion bash >> ~/.bashrc

# Zsh
jkit completion zsh >> ~/.zshrc

# Fish
jkit completion fish > ~/.config/fish/completions/jkit.fish

# PowerShell
jkit completion powershell >> $PROFILE
```
