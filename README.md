# jkit — a Jenkins CLI for developers

`jkit` is `gh` for Jenkins. Trigger builds, tail logs, inspect stages, and lint
`Jenkinsfile`s from your terminal — fast, scriptable, zero-config.

This is **not** a Jenkins admin tool. It's a developer productivity tool
that thinks in workflows, not endpoints.

```bash
jkit run my-app --wait --log     # trigger, wait, stream log
jkit status my-app               # recent builds
jkit log my-app -f               # tail live build log
jkit stages my-app               # list stages w/ IDs + paths
jkit diagnose my-app             # summarize the failure
jkit params my-app               # what params does it accept?
jkit search backend              # find jobs by name
jkit history my-app              # success rate + duration trend
jkit env my-app                  # a build's injected env vars
jkit lint                        # validate ./Jenkinsfile
jkit open my-app                 # open in browser
```

## Install

From source (Go 1.24+):

```bash
go install github.com/ysmaoui/jkit@latest
```

Or build a checkout:

```bash
git clone https://github.com/ysmaoui/jkit && cd jkit
go build -o jkit .
sudo mv jkit /usr/local/bin/
```

Pre-built binaries for Linux / macOS / Windows (amd64 + arm64) are published
on the [Releases](https://github.com/ysmaoui/jkit/releases) page once a tag is cut.

## Authenticate

```bash
jkit auth login                                              # interactive
jkit auth login --host https://jenkins.example.com \
              --user me --token "$JENKINS_TOKEN"           # non-interactive
jkit auth status
```

Credentials are stored in `~/.config/jkit/config.yml`. Multiple hosts are
supported via `--alias`.

## Zero-config job detection

Commands like `run`, `status`, `log`, and `open` resolve the job from:

1. `.jkit.yml` in the current or parent directory
2. Git remote `origin` (`org/repo`)
3. Current directory basename

```yaml
# .jkit.yml
job: team/backend/my-service
host: https://jenkins.example.com  # optional
```

Then `jkit run`, `jkit status`, `jkit log -f` Just Work.

## Documentation

- [Quickstart](docs/quickstart.md)
- [Command reference](docs/commands.md)
- [Configuration](docs/configuration.md)
- [Scripting / JSON output](docs/scripting.md)
- [Design notes](docs/DESIGN.md)
- [Contributing](CONTRIBUTING.md)

## Use with AI agents

Drop-in skill, slash command, and sub-agent that let an AI coding agent drive
`jkit` end-to-end (status, log streaming, failure triage). Authored for
[Claude Code](https://docs.claude.com/en/docs/claude-code/overview); the
prompts transfer to other agent runtimes. See [`agents/`](agents/README.md).

## Status

Early-stage. Expect breaking changes before `v1.0`. Issues and PRs welcome.

## License

[MIT](LICENSE)
