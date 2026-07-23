# Design Notes — `jkit`: A Developer-First Jenkins CLI

> Architectural and design context for `jkit`. For user docs see [README.md](../README.md)
> and [docs/quickstart.md](quickstart.md). For contributor onboarding see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Project Vision

`jkit` is a command-line interface for Jenkins, inspired by GitHub's `gh` CLI. Where `gh` transformed GitHub from a browser-first experience into a terminal-native workflow, `jkit` does the same for Jenkins. The core insight is that developers interact with Jenkins in predictable, repetitive patterns — checking build status, reading logs, triggering builds, approving inputs — and all of these should be fast, scriptable, and zero-config from the terminal.

This is NOT an admin tool. This is NOT a wrapper around the Jenkins REST API. This is a developer productivity tool that thinks in workflows, not endpoints.

---

## Technology Stack

- **Language:** Go 1.22+
- **CLI framework:** [Cobra](https://github.com/spf13/cobra) (matches `gh` patterns, excellent completion support)
- **Configuration:** [Viper](https://github.com/spf13/viper) for config file management
- **HTTP client:** Standard `net/http` with a custom Jenkins API client wrapper
- **Output formatting:** Go templates for `--format`, `encoding/json` for `--json`
- **Terminal UI:** [lipgloss](https://github.com/charmbracelet/lipgloss) for styled output, [bubbles](https://github.com/charmbracelet/bubbletea) for interactive prompts
- **Testing:** Standard `testing` package + [testify](https://github.com/stretchr/testify) for assertions
- **Build:** GoReleaser for cross-platform binaries
- **Module path:** `github.com/ysmaoui/jkit`

---

## Project Structure

```
jkit/
├── docs/DESIGN.md             # This file — architecture & design notes
├── README.md                  # User-facing documentation
├── go.mod
├── go.sum
├── main.go                    # Entrypoint — minimal, calls cmd.Execute()
├── cmd/
│   ├── root.go                # Root command, global flags (--host, --json, --format)
│   ├── auth/
│   │   ├── login.go           # jkit auth login
│   │   └── status.go          # jkit auth status
│   ├── run.go                 # jkit run <job> [-p KEY=VALUE] [--wait] [--log]
│   ├── status.go              # jkit status [job] [--all] [--branch]
│   ├── log.go                 # jkit log <job> [build#] [--follow] [--stage]
│   ├── open.go                # jkit open <job> [build#]
│   ├── lint.go                # jkit lint [Jenkinsfile]
│   ├── stages.go              # jkit stages <job> [build#]
│   ├── cancel.go              # jkit cancel <job> [build#]
│   ├── restart.go             # jkit restart <job> [build#]
│   ├── input.go               # jkit input / jkit approve / jkit deny
│   ├── queue.go               # jkit queue
│   ├── list.go                # jkit list [--folder]
│   └── artifacts.go           # jkit artifacts <job> [build#]
├── internal/
│   ├── api/
│   │   ├── client.go          # Jenkins HTTP client (auth, retries, error handling)
│   │   ├── client_test.go
│   │   ├── jobs.go            # Job-related API calls
│   │   ├── builds.go          # Build-related API calls (trigger, status, log)
│   │   ├── pipeline.go        # Pipeline-specific APIs (stages, input steps)
│   │   ├── queue.go           # Queue APIs
│   │   └── crumb.go           # CSRF crumb handling (Jenkins-specific)
│   ├── config/
│   │   ├── config.go          # Config file read/write (~/.config/jkit/)
│   │   ├── config_test.go
│   │   └── auth.go            # Credential storage and retrieval
│   ├── context/
│   │   ├── resolver.go        # Git repo → Jenkins job resolution
│   │   └── resolver_test.go
│   ├── output/
│   │   ├── formatter.go       # Table, JSON, and template output
│   │   ├── color.go           # Terminal color utilities
│   │   └── log_streamer.go    # Real-time log streaming with ANSI support
│   └── jenkins/
│       ├── types.go           # Domain types: Job, Build, Stage, QueueItem, etc.
│       └── errors.go          # Typed errors for Jenkins-specific failures
├── test/
│   ├── integration/
│   │   ├── docker-compose.yml # Jenkins instance for integration tests
│   │   ├── Jenkinsfile        # Test pipeline definition
│   │   └── integration_test.go
│   └── fixtures/              # API response fixtures for unit tests
│       ├── build_status.json
│       ├── pipeline_stages.json
│       └── job_list.json
├── scripts/
│   └── setup-test-jenkins.sh  # Bootstraps Docker Jenkins for local dev
├── .goreleaser.yml            # Cross-platform release config
└── .github/
    └── workflows/
        ├── ci.yml             # Lint, test, build on PR
        └── release.yml        # GoReleaser on tag push
```

---

## Architecture & Key Design Decisions

### Jenkins API Client (`internal/api/client.go`)

The Jenkins REST API is inconsistent across job types. The client layer MUST abstract these differences. Key responsibilities:

- **Authentication:** Support API tokens (username + token as basic auth). SSO/OAuth is out of scope for MVP.
- **CSRF crumb handling:** Jenkins requires a crumb token for POST requests. The client must fetch and cache crumbs transparently.
- **Job path normalization:** Jenkins uses URL-encoded paths with `/job/` segments. A job at folder path `team/backend/my-service` maps to URL path `/job/team/job/backend/job/my-service`. The client must handle this conversion so callers use natural paths.
- **Retry logic:** Retry on 503 (Jenkins restarting) and network errors. Exponential backoff, max 3 retries.
- **Error wrapping:** All API errors must be wrapped with context (HTTP status, Jenkins error message, URL called).

```go
// Target API for the client:
type Client struct { ... }

func NewClient(host string, auth Auth) *Client
func (c *Client) GetBuild(jobPath string, number int) (*Build, error)
func (c *Client) TriggerBuild(jobPath string, params map[string]string) (*QueueItem, error)
func (c *Client) GetBuildLog(jobPath string, number int, start int64) (*LogChunk, error)
func (c *Client) GetPipelineStages(jobPath string, number int) ([]Stage, error)
func (c *Client) SubmitInput(jobPath string, number int, inputID string, approve bool) error
```

### Jenkins API Patterns

Important Jenkins REST API patterns to implement:

```
# Job info (JSON API — append /api/json to any Jenkins URL)
GET /job/{path}/api/json?tree=name,url,color,lastBuild[number,result,timestamp]

# Build info
GET /job/{path}/{number}/api/json?tree=number,result,timestamp,duration,building

# Trigger build (requires crumb)
POST /job/{path}/build                          # no params
POST /job/{path}/buildWithParameters             # with params

# Console log (supports progressive fetching)
GET /job/{path}/{number}/logText/progressiveText?start={byte-offset}
# Response headers: X-Text-Size (current size), X-More-Data (true/false)

# Pipeline stages — Pipeline Graph View (preferred, plugin v803+)
GET /job/{path}/{number}/stages/tree
# Returns: { status, data: { complete, stages: [ {id, name, state, type,
#           pauseDurationMillis, startTimeMillis, totalDurationMillis,
#           children[], isSequential, synthetic, placeholder, agent, url } ] } }

# Pipeline stage / step log (single endpoint, accepts stage IDs and step IDs)
GET /job/{path}/{number}/stages/log?nodeId={id}

# Pipeline stages — Blue Ocean (fallback for instances without PGV ≥ 803)
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/{nodeId}/log/

# Pending input steps
GET /job/{path}/{number}/wfapi/pendingInputActions

# Submit input
POST /job/{path}/{number}/input/{inputId}/proceedEmpty  # approve
POST /job/{path}/{number}/input/{inputId}/abort          # deny

# CSRF crumb
GET /crumbIssuer/api/json

# Queue
GET /queue/api/json?tree=items[id,task[name,url],why,inQueueSince]

# Jenkinsfile validation (linting)
POST /pipeline-model-converter/validate  # body: jenkinsfile=<contents>
```

**Pipeline data sources.** Jenkins exposes pipeline stage/step data through three
different plugin generations. jkit prefers the newest:

| Generation | Plugin | Endpoint | Status |
|---|---|---|---|
| 1 | `pipeline-rest-api` | `/wfapi/**` | Older, ubiquitous. jkit does **not** use it. |
| 2 | `blueocean-rest` | `/blue/rest/...` | Deprecated. jkit uses as fallback. |
| 3 | `pipeline-graph-view` v803+ | `/stages/tree`, `/stages/log` | **Preferred.** Maintained replacement. |

Default selection is `auto` (PGV first, fall back to Blue Ocean on 404). Override
via `--pipeline-source=pgv|blueocean|auto` or `JKIT_PIPELINE_SOURCE`. PGV's
single `/stages/log?nodeId=` endpoint accepts both stage IDs and step IDs and
gives explicit `PARALLEL_BLOCK` typing for nested parallel stages — no
client-side heuristics. On instances with neither plugin available,
pipeline-detail commands degrade gracefully (basic build info still works via
the classic `/api/json` endpoints).

### Git-to-Job Resolution (`internal/context/resolver.go`)

This is the "magic" that makes `jkit status` work without arguments. Resolution order:

1. **Explicit config:** Check `.jkit.yml` in repo root for `job` field
2. **Git remote matching:** Extract org/repo from git remote URL, search Jenkins for matching multibranch pipeline jobs
3. **Job name heuristic:** Try the repo directory name as a job name
4. **Prompt:** If ambiguous, show candidates and let user pick (then offer to save to `.jkit.yml`)

The current git branch is used to resolve the specific branch build within a multibranch pipeline.

```yaml
# .jkit.yml — optional per-repo config
jenkins:
  host: jenkins.company.com     # override default host
  job: /folder/team-backend/my-service
```

### Configuration & Auth (`internal/config/`)

Config location follows XDG: `~/.config/jkit/config.yml` (or `$JKIT_CONFIG_DIR`).

```yaml
# ~/.config/jkit/config.yml
hosts:
  jenkins.company.com:
    user: jane.doe
    token: "encrypted-or-plaintext-api-token"
    default: true
  jenkins.staging.com:
    user: jane.doe
    token: "..."

defaults:
  output: table    # table | json
```

For MVP, store tokens in plaintext in the config file (with 0600 permissions). Keychain integration is a post-MVP feature. Warn users about plaintext storage during `jkit auth login`.

### Output Formatting (`internal/output/`)

All commands must support three output modes:

- **Table (default):** Human-readable, colored, truncated to terminal width
- **JSON (`--json`):** Machine-readable, complete data, no color
- **Template (`--format '{{.Number}} {{.Result}}'`):** Go template for scripting

```go
// Example: jkit status output
//
// Table mode:
// #   RESULT   DURATION  BRANCH   STARTED
// 47  ✓ pass   2m 31s    main     3 hours ago
// 46  ✗ fail   1m 12s    main     5 hours ago
// 45  ✓ pass   2m 28s    feat/x   yesterday
//
// JSON mode:
// [{"number":47,"result":"SUCCESS","duration":151000,"branch":"main",...}]
```

### Log Streaming (`internal/output/log_streamer.go`)

Jenkins exposes progressive console output via `X-Text-Size` and `X-More-Data` headers. The streamer must:

1. Poll `/logText/progressiveText?start=N` where N starts at 0
2. Print new content to stdout as it arrives
3. Advance `start` by the number of bytes **actually read**, not by `X-Text-Size`.
   Jenkins streams the whole log from `start` in one response, so a per-request
   read cap (10 MB) means a single response may not contain everything up to
   `X-Text-Size`; trusting that header as the next offset silently skips the
   unread remainder (the large-log truncation bug).
4. Keep paging while `X-More-Data` is `true` **or** unread bytes remain
   (`start < X-Text-Size`). Stop only when caught up and the build is complete.
5. Poll interval: 1 second while building, stop when complete
6. Support `Ctrl+C` to stop following without killing the process ungracefully

> Non-follow consumers (`jkit log`, `--grep`, `--tail`, `diagnose`) page the same
> way with bounded memory rather than buffering the full console. `--tail` reads
> only a server-side tail window; an unfiltered dump over `--max-bytes` is
> refused rather than truncated. See `internal/api/builds.go`.

---

## Command Specifications

### MVP Commands (implement these first, in this order)

#### 1. `jkit auth login`
```
Usage: jkit auth login [--host HOST] [--user USER] [--token TOKEN]

Interactive flow (no flags):
  1. Prompt for Jenkins URL
  2. Prompt for username
  3. Prompt for API token (masked input)
  4. Validate credentials with a test API call
  5. Save to config file
  6. Print success message

Non-interactive: all three flags provided, skip prompts.
```

#### 2. `jkit auth status`
```
Usage: jkit auth status

Output: Current host, username, and whether credentials are valid.
Exit code 1 if not authenticated.
```

#### 3. `jkit status [job] [build#]`
```
Usage: jkit status [job] [build#] [--all] [--branch BRANCH]

No args: resolve job from git context, show builds for current branch.
With job: show recent builds for that job.
With build#: show detailed status for a specific build.
--all: show builds across all branches.

Default: show last 10 builds.
```

#### 4. `jkit run [job] [-p KEY=VALUE...] [--wait] [--log]`
```
Usage: jkit run [job] [-p KEY=VALUE]... [--wait] [--log] [--branch BRANCH]

Trigger a build. If no job specified, resolve from git context.
-p: build parameters (repeatable)
--wait: block until build completes, exit code reflects result (0=success, 1=failure)
--log: implies --wait, stream logs while waiting
--branch: trigger for a specific branch (multibranch pipelines)

Output: "Build #47 triggered: https://jenkins.company.com/job/..."
With --wait: "Build #47 completed: SUCCESS (2m 31s)"
Exit codes: 0=SUCCESS, 1=FAILURE/ERROR, 2=UNSTABLE, 3=ABORTED
```

#### 5. `jkit log [job] [build#] [--follow] [--stage STAGE | --stage-id ID]`
```
Usage: jkit log [job] [build#] [--follow] [--stage STAGE] [--stage-id ID]

Show build console output. Defaults to latest build.
--follow: stream live output (poll until complete)
--stage: filter to a specific pipeline stage by name or qualified path
         (e.g. "Branch/Stage"); ambiguous bare names error with candidates
--stage-id: select a stage by exact node ID (see `jkit stages`)
--stage/--stage-id combine with --follow to tail one stage of a running build

Requires the Pipeline Graph View or Blue Ocean API for stage logs.
No build#: defaults to latest build.
If build is in progress: automatically follows unless piped to a file.
```

#### 6. `jkit open [job] [build#]`
```
Usage: jkit open [job] [build#]

Open the Jenkins page in the default browser.
No args: open the job page.
With build#: open the specific build page.
```

#### 7. `jkit lint [file]`
```
Usage: jkit lint [file]

Validate a Jenkinsfile using Jenkins' pipeline linter API.
Default file: ./Jenkinsfile
Exit code 0 if valid, 1 if errors.
Print validation errors to stderr.
```

### Post-MVP Commands (implement after MVP is validated)

- `jkit stages <job> [build#]` — list stages with node IDs and qualified paths (implemented)
- `jkit cancel <job> [build#]` — abort a running build
- `jkit restart <job> [build#]` — replay a build
- `jkit input <job> [build#]` / `jkit approve` / `jkit deny` — input step interaction
- `jkit queue` — view and manage the build queue
- `jkit list [--folder]` — list jobs
- `jkit artifacts <job> [build#]` — download build artifacts
- `jkit config set` — manage defaults

---

## Coding Conventions

### Go Style

- Follow standard Go conventions: `gofmt`, `go vet`, `golangci-lint`
- Error messages are lowercase, no trailing punctuation: `return fmt.Errorf("failed to fetch build: %w", err)`
- Use `%w` for error wrapping consistently
- Context flows through function parameters, not globals
- No `init()` functions
- Table-driven tests

### Package Rules

- `cmd/` — Cobra command definitions only. No business logic. Commands call into `internal/`.
- `internal/api/` — HTTP calls to Jenkins. Returns domain types from `internal/jenkins/types.go`.
- `internal/config/` — Reads/writes config. No HTTP calls.
- `internal/context/` — Git and job resolution. May shell out to `git`.
- `internal/output/` — Formatting and display. No business logic.
- `internal/jenkins/` — Domain types and error types only. No logic.

### Error Handling

User-facing errors should be helpful:

```go
// Bad
fmt.Errorf("404")

// Good
fmt.Errorf("job %q not found on %s — check the job path or run 'jkit list'", jobPath, host)
```

Common error scenarios to handle well:
- Not authenticated → "Run 'jkit auth login' to set up credentials"
- Job not found → "Job %q not found — run 'jkit list' to see available jobs"
- Build not found → "Build #%d not found for %q"
- Jenkins unreachable → "Cannot reach %s — check your network or VPN connection"
- CSRF crumb failure → Retry once with fresh crumb, then error
- Permission denied → "Access denied for %q — check your Jenkins permissions"

### Testing Strategy

- **Unit tests:** Mock the HTTP client at the `api.Client` level. Use `httptest.Server` for API client tests. Fixture JSON files in `test/fixtures/`.
- **Integration tests:** Docker Compose spins up a real Jenkins with a pre-configured pipeline job. Run with `go test -tags=integration ./test/integration/`. These are slow and run in CI, not on every save.
- **Test coverage target:** 80%+ on `internal/` packages. Don't obsess over `cmd/` coverage.

---

## Jenkins Compatibility

- **Target:** Jenkins LTS (latest 2 releases) + Jenkins 2.400+
- **Required plugins:** Pipeline (assumed installed on any modern Jenkins)
- **Optional plugins:** Blue Ocean (needed for `jkit stages` and stage-level logs — gracefully degrade without it)
- **Multibranch pipelines:** First-class support. This is the most common modern Jenkins setup.
- **Freestyle jobs:** Basic support (trigger, status, logs). No pipeline-specific features.
- **Folder plugin:** Support nested folder paths in job references.

---

## Distribution

- **Homebrew:** `brew install jkit` (tap initially: `brew install ysmaoui/tap/jkit`)
- **Binary releases:** GitHub Releases via GoReleaser (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64)
- **Shell completions:** Generate for bash, zsh, fish, PowerShell via Cobra's built-in support
- **Docker:** Not a priority — this is a local developer tool

---

## Development Workflow

```bash
# Setup
go mod tidy
go build -o jkit .

# Run locally
./jkit auth login
./jkit status

# Test
go test ./...
golangci-lint run

# Integration tests (requires Docker)
docker compose -f test/integration/docker-compose.yml up -d
go test -tags=integration -v ./test/integration/
docker compose -f test/integration/docker-compose.yml down

# Release (CI handles this on tag push)
git tag v0.1.0
git push origin v0.1.0
```

---

## Implementation Order

Build in this order. Each step should result in a working (if incomplete) binary.

1. **Scaffold:** `main.go`, `cmd/root.go`, Go module, basic Cobra setup, `--version` flag
2. **Config & auth:** `internal/config/`, `cmd/auth/login.go`, `cmd/auth/status.go` — get credentials working
3. **API client foundation:** `internal/api/client.go` with auth, crumb handling, error wrapping
4. **`jkit list`** (small command, validates the API client works end-to-end)
5. **`jkit status`** — requires `internal/api/builds.go` and output formatting
6. **`jkit run`** — trigger builds, with `--wait` support (requires queue polling → build polling)
7. **`jkit log`** — progressive log streaming, `--follow` mode
8. **Git context resolution** — `internal/context/resolver.go`, `.jkit.yml` support
9. **`jkit open`** — simple browser launcher
10. **`jkit lint`** — Jenkinsfile validation
11. **Shell completions, `--json`/`--format` on all commands, error message polish**
12. **GoReleaser config, Homebrew formula, README**

---

## Scope guard: observe + trigger, never mutate or elevate

`jkit` observes existing jobs and triggers builds of them. Two hard lines keep
the tool focused and safe for ordinary users:

- **Never mutates a job's definition.** Reading a job's `config.xml` is fine;
  writing it is not — job definitions belong in code (jobDSL / seed-job-as-code),
  not in an ad-hoc CLI push.
- **Never needs permissions beyond a normal build user.** Features that require
  elevated rights most users lack (e.g. pipeline *replay*) are out of scope. New
  read/discovery commands should be plain `GET`s.

Filter every proposed feature through these before building it.

## Non-Goals (explicitly out of scope)

- Jenkins administration (user management, plugin management, system config)
- Jenkins installation or upgrade
- Jenkinsfile generation or scaffolding (beyond linting)
- Job definition writes (create/update `config.xml`) — use jobDSL instead
- Pipeline replay and anything requiring rights beyond a normal build user
- Visual/TUI dashboard (keep it CLI-first, not a terminal UI app)
- Plugin system for `jkit` itself (premature — revisit after v1.0)
- Groovy script execution
- Credential management within Jenkins
- Webhook configuration

