# jk — a Jenkins CLI for developers

`jk` is `gh` for Jenkins. Trigger builds, tail logs, inspect stages, lint
`Jenkinsfile`s, and approve inputs from your terminal — fast, scriptable,
zero-config.

This is **not** a Jenkins admin tool. It's a developer productivity tool
that thinks in workflows, not endpoints.

```bash
jk run my-app --wait --log     # trigger, wait, stream log
jk status my-app               # recent builds
jk log my-app -f               # tail live build log
jk diagnose my-app             # summarize the failure
jk lint                        # validate ./Jenkinsfile
jk open my-app                 # open in browser
```

## Install

From source (Go 1.24+):

```bash
go install github.com/ysmaoui/jk@latest
```

Or build a checkout:

```bash
git clone https://github.com/ysmaoui/jk && cd jk
go build -o jk .
sudo mv jk /usr/local/bin/
```

Pre-built binaries for Linux / macOS / Windows (amd64 + arm64) are published
on the [Releases](https://github.com/ysmaoui/jk/releases) page once a tag is cut.

## Authenticate

```bash
jk auth login                                              # interactive
jk auth login --host https://jenkins.example.com \
              --user me --token "$JENKINS_TOKEN"           # non-interactive
jk auth status
```

Credentials are stored in `~/.config/jk/config.yml`. Multiple hosts are
supported via `--alias`.

## Zero-config job detection

Commands like `run`, `status`, `log`, and `open` resolve the job from:

1. `.jk.yml` in the current or parent directory
2. Git remote `origin` (`org/repo`)
3. Current directory basename

```yaml
# .jk.yml
job: team/backend/my-service
host: https://jenkins.example.com  # optional
```

Then `jk run`, `jk status`, `jk log -f` Just Work.

## Documentation

- [Quickstart](docs/quickstart.md)
- [Command reference](docs/commands.md)
- [Configuration](docs/configuration.md)
- [Scripting / JSON output](docs/scripting.md)
- [Design notes](docs/DESIGN.md)
- [Contributing](CONTRIBUTING.md)

## Use with AI agents

Drop-in skill, slash command, and sub-agent that let an AI coding agent drive
`jk` end-to-end (status, log streaming, failure triage). Authored for
[Claude Code](https://docs.claude.com/en/docs/claude-code/overview); the
prompts transfer to other agent runtimes. See [`agents/`](agents/README.md).

## Status

Early-stage. Expect breaking changes before `v1.0`. Issues and PRs welcome.

## License

[MIT](LICENSE)
