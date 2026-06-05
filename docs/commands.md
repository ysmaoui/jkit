# Command Reference

## Global Flags

All commands support these flags:

| Flag | Description |
|------|-------------|
| `--host HOST` | Override Jenkins host URL |
| `--json` | Output as JSON |
| `--format TMPL` | Output using Go template |
| `--no-color` | Disable colored output |
| `--verbose` | Show HTTP request/response details |
| `--timeout DUR` | HTTP client timeout (default `30s`) |

---

## `jkit auth login`

Authenticate with a Jenkins host.

```
jkit auth login [--host HOST] [--user USER] [--token TOKEN] [--alias ALIAS]
```

| Flag | Description |
|------|-------------|
| `--host` | Jenkins URL (prompted if omitted) |
| `--user` | Username (prompted if omitted) |
| `--token` | API token (masked prompt if omitted) |
| `--alias` | Short alias for host (e.g., `prod`, `staging`) |

- Validates credentials before saving
- First host configured becomes the default
- Config stored at `~/.config/jkit/config.yml` (see [configuration](configuration.md))

```bash
jkit auth login                                        # interactive
jkit auth login --host https://ci.co --user me --token abc  # non-interactive
jkit auth login --host https://ci.co --user me --token abc --alias prod
```

---

## `jkit auth status`

Show authentication status.

```
jkit auth status [--host HOST]
```

Exits 0 if valid, 1 if invalid.

```
Host:  https://jenkins.company.com
User:  jane
Auth:  valid
```

---

## `jkit list`

List Jenkins jobs.

```
jkit list [--folder FOLDER] [-r|--recursive]
```

| Flag | Description |
|------|-------------|
| `--folder` | Folder path to list jobs within |
| `-r, --recursive` | List jobs recursively across all folders |

Table columns: NAME, STATUS, LAST BUILD. Folders shown with trailing `/`.

```bash
jkit list                          # all top-level jobs
jkit list --folder team/frontend   # jobs in folder
jkit list -r                       # all jobs recursively
jkit list --json                   # JSON output
jkit list --format '{{range .}}{{.Name}}{{"\n"}}{{end}}'
```

---

## `jkit status`

Show build status.

```
jkit status [job] [build#] [--limit N]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--limit` | Number of recent builds | 10 |

**List mode** (no build#) — shows recent builds:

```
#   RESULT    DURATION  STARTED
47  SUCCESS   2m31s     Jan 02 15:04
46  FAILURE   1m12s     Jan 02 14:00
```

**Detail mode** (with build#) — shows build info, parameters, and pipeline stages:

```
Build:    #47
Result:   SUCCESS
Duration: 2m31s
Started:  Jan 02 15:04:05
URL:      https://jenkins.company.com/job/my-app/47

Stages:
  Build       SUCCESS   45s
  Test        SUCCESS   1m3s
  Deploy      SUCCESS   42s
```

Stages displayed for pipeline jobs via Blue Ocean REST API.

```bash
jkit status my-app            # last 10 builds
jkit status my-app --limit 3  # last 3 builds
jkit status my-app 47         # build detail
jkit status my-app 47 --json  # JSON detail
```

---

## `jkit run`

Trigger a build.

```
jkit run [job] [-p KEY=VALUE]... [--wait] [--log]
```

| Flag | Description |
|------|-------------|
| `-p KEY=VALUE` | Build parameter (repeatable) |
| `--wait` | Wait for build to complete |
| `--log` | Stream build log (implies `--wait`) |

**Exit codes** (with `--wait`):

| Code | Meaning |
|------|---------|
| 0 | SUCCESS |
| 1 | FAILURE |
| 2 | UNSTABLE |
| 3 | ABORTED |
| 4 | Unknown result |

Progress messages go to stderr, log output to stdout. Ctrl+C interrupts gracefully. Queue timeout: 5 minutes. Build timeout: 2 hours.

```bash
jkit run my-app                              # fire and forget
jkit run my-app -p BRANCH=main -p ENV=prod   # with parameters
jkit run my-app --wait                       # wait for result
jkit run my-app --wait --log                 # wait + stream log
jkit run --log                               # auto-detect job
```

---

## `jkit log`

View build console output.

```
jkit log [job] [build#] [-f|--follow] [--stage STAGE] [--stage-id ID] [--grep PATTERN] [--tail N] [--head N] [--max-bytes N]
```

| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow live output |
| `--stage` | Show log for a stage by name or qualified path (e.g. `"Branch/Stage"`) |
| `--stage-id` | Show log for a stage by exact node ID (see `jkit stages`) |
| `--grep` | Filter log lines matching pattern |
| `-i, --ignore-case` | Case-insensitive `--grep` matching |
| `--tail N` | Show only the last N lines |
| `--head N` | Show only the first N lines |
| `--max-bytes N` | Refuse to dump an unfiltered console larger than N bytes (default 50 MB; `0` = unlimited) |

- Defaults to latest build if no build# given
- Auto-follows if build is in progress (disabled when `--grep`, `--tail`, or `--head` active)
- Large logs are handled without buffering the whole console in memory:
  - `--tail N` fetches only a tail window from the server (cheap even on multi-GB logs)
  - `--head N` stops reading once N lines are seen
  - `--grep` streams the full log with bounded memory and exits early under `--head`
  - an unfiltered `jkit log` over `--max-bytes` is refused with guidance (use `--tail`/`--head`/`--grep`, redirect, or `--max-bytes 0`) rather than silently truncated
- `--stage` requires the Pipeline Graph View or Blue Ocean plugin
- When a bare `--stage` name matches multiple stages (e.g. the same stage in two
  parallel branches), the command errors and lists each candidate's qualified
  path and ID. Pass a qualified path (`--stage "RemoteExec/Run Bazel Build"`) or
  `--stage-id` to disambiguate.
- `--stage`/`--stage-id` combine with `-f` to tail a single stage of a running
  build; `--stage` and `--stage-id` are mutually exclusive
- `--tail` and `--head` are incompatible with `--follow`

```bash
jkit log my-app                                  # latest build, full log
jkit log my-app 42                               # specific build
jkit log my-app -f                               # follow live
jkit log my-app --stage Build                    # specific stage log
jkit log my-app --stage "RemoteExec/Run Bazel Build"  # disambiguate by branch
jkit log my-app --stage-id 17 -f                 # tail one stage by ID
jkit log my-app --grep ERROR                     # filter lines
jkit log my-app --grep error -i                  # case-insensitive filter
jkit log my-app --tail 50                        # last 50 lines (tail window, cheap)
jkit log my-app --head 20                        # first 20 lines (stops early)
jkit log my-app --max-bytes 0 > build.log        # force a full dump to a file
```

---

## `jkit stages`

List the pipeline stages of a build with their node IDs and qualified paths.

```
jkit stages [job] [build#]
```

- Defaults to latest build if no build# given
- Requires the Pipeline Graph View or Blue Ocean plugin
- The `STAGE` column shows a qualified path that disambiguates duplicate names
  across parallel branches (e.g. `RemoteExec/Run Bazel Build`)
- Feed a path to `jkit log --stage` or an ID to `jkit log --stage-id`
- Honors `--json` / `--format` for scripting (the JSON includes `id` and `path`)

```bash
jkit stages my-app             # latest build
jkit stages my-app 42          # specific build
jkit stages my-app 42 --json   # machine-readable (id, name, path, type, status)
```

---

## `jkit open`

Open Jenkins page in browser.

```
jkit open [job] [build#]
```

- No build# opens the job page
- With build# opens the build page
- Accepts full Jenkins URLs directly
- Works on Linux (`xdg-open`), macOS (`open`), Windows (`rundll32`)

```bash
jkit open                  # current job (from .jkit.yml or context)
jkit open my-app           # job page
jkit open my-app 47        # build page
```

---

## `jkit abort`

Abort a running build.

```
jkit abort [job] [build#] [-w|--wait]
```

| Flag | Description |
|------|-------------|
| `-w, --wait` | Wait until the build actually stops |

Defaults to the latest build. Checks if the build is running before sending the stop signal.

```bash
jkit abort my-app              # abort latest build
jkit abort my-app 42           # abort specific build
jkit abort my-app 42 --wait    # abort and wait for it to stop
```

---

## `jkit rebuild`

Retrigger a build with the same parameters.

```
jkit rebuild [job] [build#] [--wait] [--log]
```

| Flag | Description |
|------|-------------|
| `--wait` | Wait for build to complete |
| `--log` | Stream build log (implies `--wait`) |

Defaults to the latest build. Retrieves the source build's parameters and triggers a new build with the same values. Exit codes match `jkit run --wait`.

```bash
jkit rebuild my-app 42            # retrigger build 42
jkit rebuild my-app 42 --wait     # retrigger and wait
jkit rebuild my-app 42 --log      # retrigger and stream log
```

---

## `jkit test`

Show test results.

```
jkit test [job] [build#] [--failed] [--new-failures]
```

| Flag | Description |
|------|-------------|
| `--failed` | Show only failed tests |
| `--new-failures` | Show only tests that regressed from the previous build |

Defaults to the latest build. Displays test cases with class, name, status, and duration. Failed tests include error details. Summary shows pass/fail/skip counts.

```bash
jkit test my-app                   # all test results
jkit test my-app 42                # specific build
jkit test my-app 42 --failed       # only failures
jkit test my-app --new-failures    # regressions from previous build
```

---

## `jkit artifacts`

List or download build artifacts.

```
jkit artifacts [job] [build#] [-d FILENAME] [-o PATH]
```

| Flag | Description |
|------|-------------|
| `-d, --download` | Download artifact by filename |
| `-o, --output` | Output file path (default: original filename in cwd) |

Defaults to the latest build. Without `-d`, lists artifacts as a table.

```bash
jkit artifacts my-app 42                          # list artifacts
jkit artifacts my-app 42 -d report.xml            # download to ./report.xml
jkit artifacts my-app 42 -d report.xml -o /tmp/   # download to /tmp/report.xml
```

---

## `jkit changes`

Show SCM changes in a build.

```
jkit changes [job] [build#]
```

Defaults to the latest build. Displays commit hash (7 chars), author, and message.

```bash
jkit changes my-app               # latest build changes
jkit changes my-app 42            # specific build
jkit changes my-app --json        # JSON output
```

---

## `jkit diagnose`

Analyze a failed build and show failure summary.

```
jkit diagnose [job] [build#]
```

Fetches build metadata, identifies failed stages, extracts error lines, and shows commits and parameters. Defaults to the latest build. Accepts full Jenkins URLs.

```bash
jkit diagnose my-app 42
jkit diagnose https://jenkins.example.com/job/team/job/svc/42/
jkit diagnose my-app --json
```

---

## `jkit diff`

Compare two builds of the same job.

```
jkit diff [job] [build1] [build2]
```

Shows differences in parameters, stage outcomes, test results, and commits between two builds.

- Two build numbers: compares them directly
- One build number: compares with the previous build
- No build numbers: compares the latest two builds

```bash
jkit diff my-app 41 42        # compare builds 41 and 42
jkit diff my-app 42           # compare build 42 with build 41
jkit diff my-app              # compare latest two builds
```

---

## `jkit queue`

Show pending builds in the queue.

```
jkit queue [--job FILTER]
```

| Flag | Description |
|------|-------------|
| `--job` | Filter queue items by job name (substring match) |

Displays queue ID, job name, and reason for each pending build.

```bash
jkit queue                    # all queued builds
jkit queue --job my-app       # filter by job name
jkit queue --json             # JSON output
```

### `jkit queue cancel`

Cancel a queued build.

```
jkit queue cancel <queue-id>
```

```bash
jkit queue cancel 12345
```

---

## `jkit config`

Manage CLI configuration.

### `jkit config list`

Show all configured hosts with user, alias, and default status.

```bash
jkit config list
```

### `jkit config set-default`

Set the default Jenkins host.

```bash
jkit config set-default https://jenkins.prod.com
jkit config set-default prod    # by alias
```

### `jkit config remove`

Remove a configured host.

```bash
jkit config remove https://jenkins.staging.com
jkit config remove staging    # by alias
```

### `jkit config set-alias`

Set or change an alias for a host.

```bash
jkit config set-alias https://jenkins.prod.com prod
```

---

## `jkit lint`

Validate a declarative Jenkinsfile against Jenkins. Scripted pipelines (`node { }`) are not supported.

```
jkit lint [file]
```

- Defaults to `./Jenkinsfile` if no file given
- Uses Jenkins pipeline-model-converter validate endpoint
- Exit 0 if valid, exit 1 with error details if invalid

```bash
jkit lint                         # validate ./Jenkinsfile
jkit lint path/to/Jenkinsfile     # validate specific file
```

---

## `jkit completion`

Generate shell completion script.

```
jkit completion [bash|zsh|fish|powershell]
```

```bash
jkit completion bash >> ~/.bashrc
jkit completion zsh >> ~/.zshrc
jkit completion fish > ~/.config/fish/completions/jkit.fish
jkit completion powershell >> $PROFILE
```
