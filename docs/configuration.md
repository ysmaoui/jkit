# Configuration

## Config File

**Location:** `~/.config/jk/config.yml` (0600 permissions)

```yaml
hosts:
  https://jenkins.company.com:
    user: jane
    token: "api-token-here"
    default: true
  https://jenkins.staging.com:
    user: jane
    token: "staging-token"
```

Created automatically by `jk auth login`. First host added becomes the default.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `JK_CONFIG_DIR` | Override config directory | — |
| `XDG_CONFIG_HOME` | XDG base directory | `~/.config` |
| `NO_COLOR` | Disable colored output (any value) | — |
| `JK_PIPELINE_SOURCE` | Pipeline backend: `auto` (PGV→Blue Ocean), `pgv`, `blueocean` | `auto` |

## Pipeline backend

Stage trees and stage logs are served by two plugins:

- **Pipeline Graph View** (v803+) — preferred. Native tree, step-level logs, actively maintained.
- **Blue Ocean** — fallback. Deprecated but widely installed.

Default is `auto`: try PGV first, fall back to Blue Ocean on 404. Override with
`--pipeline-source=pgv|blueocean|auto` or the env var above. PGV is typically
slower per request but the payload is smaller and the tree model is simpler.

Config directory precedence: `JK_CONFIG_DIR` > `$XDG_CONFIG_HOME/jk` > `~/.config/jk`

## Per-Repo Config (`.jk.yml`)

Place `.jk.yml` in your repo root to bind it to a Jenkins job:

```yaml
job: team/backend/my-service
host: https://jenkins.company.com  # optional, overrides default host
```

jk searches upward from cwd until it finds `.jk.yml` or reaches filesystem root.

With this file, commands work without arguments:

```bash
cd my-service/
jk run          # triggers team/backend/my-service
jk status       # shows its builds
jk log -f       # streams its latest log
```

## Job Context Resolution

When no job argument is given, jk resolves automatically:

| Priority | Source | Example result |
|----------|--------|---------------|
| 1 | `.jk.yml` `job` field | `team/backend/my-service` |
| 2 | Git remote `origin` | `your-org/my-service` |
| 3 | Current directory name | `my-service` |

Git remote parsing supports:
- SSH: `git@github.com:org/repo.git` → `org/repo`
- HTTPS: `https://github.com/org/repo.git` → `org/repo`

An explicit `[job]` argument always overrides auto-detection.

## Multi-Host Setup

Add multiple hosts via `jk auth login`:

```bash
jk auth login --host https://jenkins.prod.com --user jane --token $PROD_TOKEN --alias prod
jk auth login --host https://jenkins.staging.com --user jane --token $STG_TOKEN --alias staging
```

The first host configured is the default. Override per-command:

```bash
jk list --host https://jenkins.staging.com
```

Or override per-repo via `.jk.yml`:

```yaml
job: my-app
host: https://jenkins.staging.com
```

## Managing Hosts

```bash
jk config list                              # show all hosts
jk config set-default prod                  # change default (by alias or URL)
jk config set-alias https://ci.co staging   # set alias on existing host
jk config remove staging                    # remove host (by alias or URL)
```
