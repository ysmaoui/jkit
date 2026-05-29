---
name: jenkins
description: Query Jenkins builds, fetch stage logs, inspect pipeline metadata via the `jkit` CLI. Use for CI/CD debugging, build analysis, Jenkins automation.
allowed-tools: Bash(command:jkit*)
---

# Jenkins CLI (`jkit`)

Jenkins operations via the [`jkit`](https://github.com/ysmaoui/jkit) CLI. Auth is
stored in `~/.config/jkit/config.yml` — run `jkit auth login` once before first use.

---

## Quick Reference

All commands accept a Jenkins URL as first arg, or `job [build#]` positional args.

```bash
# Build status (detail: params, stages, cause)
jkit status URL
jkit status my-job 42
jkit status my-job --limit 5        # recent builds

# Pipeline stages (node IDs + qualified paths)
jkit stages URL                     # list stages: ID, path, type, status
jkit stages URL --json              # machine-readable (id, name, path, …)

# Build log
jkit log URL                        # full console
jkit log URL --stage "Build"        # stage log by name
jkit log URL --stage "Linux/Test"   # by qualified path (disambiguates branches)
jkit log URL --stage-id 6710        # by exact node ID (from `jkit stages`)
jkit log URL --stage "Test" -f      # follow one stage of a running build
jkit log URL --tail 50              # last 50 lines
jkit log URL --grep "ERROR" -i      # filter lines
jkit log -f my-job                  # stream (follow)

# Failure diagnosis (errors, failed stages, params, commits)
jkit diagnose URL
jkit diagnose URL --json

# Compare two builds
jkit diff my-job 41 42              # explicit
jkit diff my-job 42                 # vs previous
jkit diff my-job                    # latest two

# Test results
jkit test URL                       # all tests
jkit test URL --failed              # failures only
jkit test URL --new-failures        # regressions only

# SCM changes (commits)
jkit changes URL

# Trigger build
jkit run my-job -p KEY=VAL --wait --log

# Rebuild with same params
jkit rebuild my-job 42 --wait --log

# Abort running build
jkit abort my-job 42 --wait

# Artifacts
jkit artifacts URL                  # list
jkit artifacts URL -d report.xml    # download

# Queue
jkit queue                          # pending builds
jkit queue --job my-job             # filter
jkit queue cancel 12345             # cancel

# List jobs
jkit list                           # current folder
jkit list -r                        # recursive

# Open in browser
jkit open my-job 42
```

---

## Global Flags

| Flag | Description |
|---|---|
| `--json` | Structured JSON output |
| `--format TMPL` | Go template output |
| `--host URL` | Override Jenkins host (or alias) |
| `--verbose` | Show HTTP request/response on stderr |
| `--timeout DUR` | HTTP timeout (default 30s) |
| `--no-color` | Disable ANSI colors |

---

## Build Failure Analysis Workflow

1. `jkit diagnose URL` — overview: errors, failed stages, params, commits
2. `jkit log URL --stage "StageName"` — full stage log for failed stage. If the
   name is ambiguous (same stage in multiple parallel branches), the command
   lists every candidate with its qualified path and ID — re-run with the
   qualified path (`--stage "Branch/StageName"`) or `--stage-id <id>`. Use
   `jkit stages URL` to see all stages and IDs up front.
3. `jkit test URL --failed` — if UNSTABLE, show test failures
4. `jkit test URL --new-failures` — regressions vs previous build
5. `jkit changes URL` — what commits triggered the build
6. `jkit diff my-job 41 42` — compare with last good build

---

## URL Input

Paste any Jenkins URL — classic or Blue Ocean. The CLI auto-extracts host, job path, build number.

```
Classic:    https://jenkins.example.com/job/team/job/svc/42/
Blue Ocean: https://jenkins.example.com/blue/organizations/jenkins/team%2Fsvc/detail/main/42/pipeline
Console:    https://jenkins.example.com/job/team/job/svc/42/console
```

---

## Diagnosing Stuck/Hanging Builds

When a build is BUILDING but appears stuck:

1. **Never run a full `jkit log` in background on BUILDING jobs** — it hangs waiting for more output. Instead tail a single stage with `jkit log URL --stage "Stage" -f` (or `--stage-id <id>`), which returns when that stage finishes, or run a one-shot `jkit log --stage` on an already-completed stage. Use `jkit stages URL` to find the running stage's ID.
2. **Find the last log line, then reason about the code** — identify what step executes *after* the last visible output. That's where it's stuck.
3. **Pod YAML printed = pod is running.** The hang is in whatever step follows pod allocation (e.g. a shell step in the wrong container), not in pod scheduling.
4. **Wait for diagnostic commands to return before concluding.** If a command hasn't returned data yet, don't move on — either wait or try a different approach.

---

## Troubleshooting

| Error | Fix |
|---|---|
| 404 Not Found | Check URL/job path. Branch names need URL encoding (`feature/x` → `feature%2Fx`) |
| 401 Unauthorized | Run `jkit auth login` to re-authenticate |
| 403 Forbidden | User lacks Jenkins permissions for this job |
| 5xx Server Error | Retry. CLI auto-retries transient 502/503/504 |
| No test results | JUnit plugin not configured, or build has no tests |
| Stage log empty | Pipeline Graph View or Blue Ocean plugin required for stage-level logs |
| `stage "X" is ambiguous` | Same name in multiple parallel branches — re-run with the qualified path (`--stage "Branch/X"`) or `--stage-id` from `jkit stages` |
