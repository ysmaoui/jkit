# Contributing

## Prerequisites

- Go 1.24+
- Docker + Docker Compose (for integration tests)

## Build & Test

```bash
go build -o jkit .          # build binary
make test                  # unit tests
make integration-test      # Docker Compose integration tests (starts Jenkins, runs 18 assertions, tears down)
```

Integration test targets:

```bash
make integration-up        # start Jenkins container (waits for healthy)
make integration-down      # stop and remove container
make integration-test      # up → run tests → down (cleanup on failure)
```

Port defaults to 9090. Override with `JENKINS_PORT=8888 make integration-test`.

## Project Structure

```
main.go                     # entrypoint
cmd/
  root.go                   # root command, global flags (--host, --json, --format, --verbose, --timeout), version
  list.go                   # jkit list
  status.go                 # jkit status
  run.go                    # jkit run
  log.go                    # jkit log
  open.go                   # jkit open
  lint.go                   # jkit lint
  abort.go                  # jkit abort
  rebuild.go                # jkit rebuild
  test.go                   # jkit test
  artifacts.go              # jkit artifacts
  changes.go                # jkit changes
  diagnose.go               # jkit diagnose
  diff.go                   # jkit diff
  queue.go                  # jkit queue / jkit queue cancel
  config.go                 # jkit config list/set-default/remove/set-alias
  completion.go             # jkit completion
  factory.go                # clientFromCmd — creates API client from cobra flags
  helpers.go                # shared helpers (formatDuration, newFetchLog, resolveJobArgs)
  auth/
    auth.go                 # auth subcommand group
    login.go                # jkit auth login
    status.go               # jkit auth status
internal/
  api/
    client.go               # HTTP client, auth transport, cookie jar, retry, crumb handling
    crumb.go                # CSRF crumb fetch/cache/invalidate
    jobs.go                 # ListJobs, GetJob
    builds.go               # TriggerBuild, GetBuild, GetBuilds, GetBuildLog, GetQueueItem, StopBuild
    pipeline.go             # GetPipelineStages, GetStageLog (Blue Ocean REST API)
  config/
    config.go               # YAML config load/save, XDG path resolution
  context/
    resolver.go             # Job auto-detection: .jkit.yml → git remote → dirname
    url_parser.go           # Parse Jenkins URLs (classic + Blue Ocean)
  jenkins/
    types.go                # Domain types: Job, Build, Stage, QueueItem, LogChunk, TestReport, etc.
    errors.go               # Typed errors: AuthError, NotFoundError, PermissionError, etc.
    stage_tree.go           # BuildStageTree — flat stages to depth-annotated tree
  output/
    formatter.go            # Table, JSON, Go template output
    color.go                # ANSI color for build statuses
    log_streamer.go         # Progressive log streaming with polling
integration/
  docker-compose.yml        # Jenkins container config
  run.sh                    # Bash test runner
  jenkins/
    Dockerfile              # Jenkins image with plugins + config
    plugins.txt             # Required Jenkins plugins
    casc.yml                # Jenkins Configuration as Code
    init.groovy.d/          # Groovy scripts to seed test jobs
```

## Adding a Command

1. Create `cmd/foo.go`:

```go
package cmd

import "github.com/spf13/cobra"

var fooCmd = &cobra.Command{
    Use:   "foo [args]",
    Short: "Do something",
    RunE:  runFoo,
}

func init() {
    fooCmd.Flags().String("bar", "", "Some flag")
    rootCmd.AddCommand(fooCmd)
}

func runFoo(cmd *cobra.Command, args []string) error {
    client, _, err := clientFromCmd(cmd)
    if err != nil {
        return err
    }
    // Use client to call Jenkins API
    // Use output.NewFormatter for output
    return nil
}
```

2. `clientFromCmd(cmd)` in `factory.go` loads config + creates an authenticated API client from the `--host` flag or default host.

3. Use `output.NewFormatter(os.Stdout, isJSON, tmpl)` for table/JSON/template output.

## Error Handling

Return typed errors from `internal/jenkins/errors.go`:

| Error | HTTP | Message |
|-------|------|---------|
| `AuthError` | 401 | `not authenticated to {host} — run 'jkit auth login'` |
| `NotFoundError` | 404 | `{resource} not found on {host} — run 'jkit list'` |
| `PermissionError` | 403 | `access denied for {resource} on {host}` |
| `UnreachableError` | network | `cannot reach {host} — check network or VPN` |
| `ServerError` | 5xx | `Jenkins error HTTP {code} on {path}` |
| `ExitError` | — | Custom exit code + message |

`ExitError` controls the process exit code (used by `run --wait` and `lint`).

## Output Conventions

- **stderr** — progress messages, warnings, prompts (`fmt.Fprintf(os.Stderr, ...)`)
- **stdout** — data output (tables, JSON, logs, URLs)
- **Color** — auto-disabled when piped or `NO_COLOR` set; use `output.ColorStatus()` for build statuses
