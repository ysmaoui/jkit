---
name: jenkins
description: Query Jenkins builds, fetch stage logs, inspect pipeline metadata via the `jk` CLI. Use for CI/CD debugging, build analysis, Jenkins automation.
allowed-tools: Bash(command:jk*)
---

# Jenkins CLI (`jk`)

Jenkins operations via the [`jk`](https://github.com/ysmaoui/jk) CLI. Auth is
stored in `~/.config/jk/config.yml` — run `jk auth login` once before first use.

---

## Quick Reference

All commands accept a Jenkins URL as first arg, or `job [build#]` positional args.

```bash
# Build status (detail: params, stages, cause)
jk status URL
jk status my-job 42
jk status my-job --limit 5        # recent builds

# Build log
jk log URL                        # full console
jk log URL --stage "Build"        # stage log
jk log URL --tail 50              # last 50 lines
jk log URL --grep "ERROR" -i      # filter lines
jk log -f my-job                  # stream (follow)

# Failure diagnosis (errors, failed stages, params, commits)
jk diagnose URL
jk diagnose URL --json

# Compare two builds
jk diff my-job 41 42              # explicit
jk diff my-job 42                 # vs previous
jk diff my-job                    # latest two

# Test results
jk test URL                       # all tests
jk test URL --failed              # failures only
jk test URL --new-failures        # regressions only

# SCM changes (commits)
jk changes URL

# Trigger build
jk run my-job -p KEY=VAL --wait --log

# Rebuild with same params
jk rebuild my-job 42 --wait --log

# Abort running build
jk abort my-job 42 --wait

# Artifacts
jk artifacts URL                  # list
jk artifacts URL -d report.xml    # download

# Queue
jk queue                          # pending builds
jk queue --job my-job             # filter
jk queue cancel 12345             # cancel

# List jobs
jk list                           # current folder
jk list -r                        # recursive

# Open in browser
jk open my-job 42
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

1. `jk diagnose URL` — overview: errors, failed stages, params, commits
2. `jk log URL --stage "StageName"` — full stage log for failed stage
3. `jk test URL --failed` — if UNSTABLE, show test failures
4. `jk test URL --new-failures` — regressions vs previous build
5. `jk changes URL` — what commits triggered the build
6. `jk diff my-job 41 42` — compare with last good build

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

1. **Never run `jk log` in background on BUILDING jobs** — it hangs waiting for more output. Use a timeout or `jk log --stage` on completed stages instead.
2. **Find the last log line, then reason about the code** — identify what step executes *after* the last visible output. That's where it's stuck.
3. **Pod YAML printed = pod is running.** The hang is in whatever step follows pod allocation (e.g. a shell step in the wrong container), not in pod scheduling.
4. **Wait for diagnostic commands to return before concluding.** If a command hasn't returned data yet, don't move on — either wait or try a different approach.

---

## Troubleshooting

| Error | Fix |
|---|---|
| 404 Not Found | Check URL/job path. Branch names need URL encoding (`feature/x` → `feature%2Fx`) |
| 401 Unauthorized | Run `jk auth login` to re-authenticate |
| 403 Forbidden | User lacks Jenkins permissions for this job |
| 5xx Server Error | Retry. CLI auto-retries transient 502/503/504 |
| No test results | JUnit plugin not configured, or build has no tests |
| Stage log empty | Blue Ocean plugin required for stage-level logs |
