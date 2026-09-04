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
The build number may sit in the URL or come after it as a separate argument
(`jkit status URL 42`); giving both with different numbers is an error.
For a **multibranch pipeline**, the `job build#` form needs the branch via
`--branch` (a URL already includes it) — see [Multibranch pipelines](#multibranch-pipelines).

```bash
# Build status (detail: params, stages, cause)
jkit status URL
jkit status my-job 42
jkit status my-job --limit 5        # recent builds

# Build trend (success rate over the window, last-vs-median duration)
jkit history my-job                 # last 20 builds
jkit history my-job --limit 50

# Pipeline stages (node IDs + qualified paths)
jkit stages URL                     # list stages: ID, path, type, status
jkit stages URL --json              # machine-readable (id, name, path, …)

# Build log  (--tail/--head/--grep cover the ENTIRE log — no head/tail-buffer limit)
jkit log URL --tail 50              # last 50 lines (cheap server-side tail window)
jkit log URL --head 20              # first 20 lines (stops reading early)
jkit log URL --grep "ERROR" -i      # search the WHOLE console for matches
jkit log URL --stage "Build"        # stage log by name
jkit log URL --stage "Linux/Test"   # by qualified path (disambiguates branches)
jkit log URL --stage-id 6710        # by exact node ID (from `jkit stages`)
jkit log URL --stage "Test" -f      # follow one stage of a running build
jkit log -f my-job                  # stream (follow)
jkit log URL                        # full console — refused if >50MB; use --tail/--grep/--head, redirect, or --max-bytes 0

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

# Injected env vars (EnvInject plugin) — secret-looking values masked
jkit env URL                        # last build if no build# given
jkit env URL --filter GIT           # names containing GIT
jkit env URL --show-secrets         # reveal masked values

# Parameters a job accepts (name, type, default, choices)
jkit params my-job                  # what to pass to `jkit run -p`

# Job definition from config.xml (Jenkinsfile, repo, discovery, retention)
jkit inspect my-job                 # why a branch does not build / which Jenkinsfile ran
jkit inspect team/svc --branch feature/x
jkit inspect my-job --show-secrets    # reveal a credential embedded in the SCM url
jkit inspect my-job --xml            # raw config.xml, for fields the summary omits

# Job config change log: "it worked last week, what changed?"
jkit inspect my-job --history       # who changed the job config, and when
jkit inspect my-job --history --show-system   # include automated re-index writes

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

# Find jobs by name (case-insensitive substring, whole instance)
jkit search backend
jkit search deploy --folder team --limit 50

# Validate a declarative Jenkinsfile against the server
jkit lint                           # ./Jenkinsfile
jkit lint path/to/Jenkinsfile

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
| `--branch NAME` | Branch of a multibranch pipeline job (e.g. `feature/x`); slashes encoded automatically. Not needed when passing a URL |
| `--verbose` | Show HTTP request/response on stderr |
| `--timeout DUR` | HTTP timeout (default 30s) |
| `--pipeline-source` | Pipeline backend: `auto` (default), `pgv`, `blueocean` (env `JKIT_PIPELINE_SOURCE`) |
| `--no-color` | Disable ANSI colors |

---

## Reading `--json` output

**Check `building` before `result`.** Jenkins serves a result on in-progress
builds, so `"result": "SUCCESS"` next to `"building": true` is normal and tells
you nothing: a pipeline that assigns `currentBuild.result` stamps a value that
afterwards only ever worsens. A build is finished only when `building` is false.

```bash
jkit status URL --json | jq -r 'if .building then "BUILDING" else .result end'
```

Text output already collapses the two fields and prints `BUILDING`; `--json`
and `--format` expose them raw. The same applies to a running build's
`duration`, which Jenkins reports as `0` until the build is finalized.

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

**Searching a large console:** `--grep` streams the *entire* log (even multi-GB
ones) with bounded memory — use it to find an error anywhere, e.g.
`jkit log URL -i --grep "exception"`. `--tail N` reads the true end of the log,
not a buffer. Don't assume `jkit log` only returns the head/tail or fabricate
console-tail workarounds; a plain unfiltered dump is *refused* for big logs, so
always narrow with `--grep`/`--tail`/`--head` (matches are line-oriented
substring matches, not regex — grep a single distinctive token, not a
multi-line phrase).

---

## URL Input

Paste any Jenkins URL — classic or Blue Ocean. The CLI auto-extracts host, job path, build number.

```
Classic:    https://jenkins.example.com/job/team/job/svc/42/
Blue Ocean: https://jenkins.example.com/blue/organizations/jenkins/team%2Fsvc/detail/main/42/pipeline
Console:    https://jenkins.example.com/job/team/job/svc/42/console
```

---

## Multibranch pipelines

A multibranch pipeline job (e.g. `team/svc`) is a *container* — only its branch
child-jobs have builds. Two ways to address a branch build:

```bash
# 1. Full URL (branch is already encoded in the path) — works as-is
jkit log "https://jenkins.example.com/job/team/job/svc/job/feature%2Fx/42/"

# 2. job build# + --branch (slashes in the branch are encoded for you)
jkit log team/svc 42 --branch feature/x
jkit diagnose team/svc 42 --branch feature/x
```

Don't hand-encode the branch into the `job` arg (`team/svc/feature%2Fx`) — pass
`--branch` instead. If you target the container without a branch, the command
returns an error **listing the available branches** — pick one and re-run with
`--branch`. To browse branches up front: `jkit list --folder team/svc`.

`jkit inspect team/svc` answers why a branch is missing or not building. It
reads `config.xml`, which is the only source for multibranch discovery rules,
and prints the repository, the discovery traits with their strategy ids in
words, the build strategies, the re-indexing schedule and the build discarder.
Anything it cannot decode is flagged `!` rather than dropped, and an absent
section says what its absence means. Run it on the container: a branch child
only carries its own script path, remote and retention, and names its parent.

`jkit inspect <job> --history` is the other half: it lists who changed the job's
configuration and when, from the JobConfigHistory plugin. Read the output with
three limits in mind. Re-indexing rewrites a branch job's config on every scan,
so on a branch child every entry is a SYSTEM write and runs of them are
collapsed into one row (`--show-system` expands them). The entry count is
retention, not a change count: the plugin caps entries per job and the server
can truncate the response silently. An empty result means either nothing
changed or you lack Job/Configure, because the plugin answers a permission
failure with an empty list rather than a 403. If the plugin is not installed the
command says so by name instead of reporting a missing job.

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
| 404 Not Found | Check URL/job path. For a multibranch job, pass the branch with `--branch feature/x` (don't hand-encode it). Aiming the `job build#` form at a multibranch/folder container returns an error listing its branches — re-run with one |
| 401 Unauthorized | Run `jkit auth login` to re-authenticate |
| 403 Forbidden | User lacks Jenkins permissions for this job |
| 5xx Server Error | Retry. CLI auto-retries transient 502/503/504 |
| No test results | JUnit plugin not configured, or build has no tests |
| `--history`: plugin not installed | The JobConfigHistory plugin is missing on that controller; no config change log exists there |
| `--history`: no config history | Either nothing changed, or you lack Job/Configure — the plugin returns an empty list instead of refusing |
| Stage log empty / `no stages found` | Pipeline Graph View or Blue Ocean plugin required for stage-level logs. If you targeted a multibranch container, the error instead lists its branches — re-run with `--branch` |
| `console log is … — refusing to dump it whole` | Log exceeds `--max-bytes` (default 50MB). Narrow with `--tail`/`--head`/`--grep`, redirect to a file, or pass `--max-bytes 0` |
| `stage "X" is ambiguous` | Same name in multiple parallel branches — re-run with the qualified path (`--stage "Branch/X"`) or `--stage-id` from `jkit stages` |
