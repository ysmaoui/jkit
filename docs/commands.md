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

## `jk auth login`

Authenticate with a Jenkins host.

```
jk auth login [--host HOST] [--user USER] [--token TOKEN] [--alias ALIAS]
```

| Flag | Description |
|------|-------------|
| `--host` | Jenkins URL (prompted if omitted) |
| `--user` | Username (prompted if omitted) |
| `--token` | API token (masked prompt if omitted) |
| `--alias` | Short alias for host (e.g., `prod`, `staging`) |

- Validates credentials before saving
- First host configured becomes the default
- Config stored at `~/.config/jk/config.yml` (see [configuration](configuration.md))

```bash
jk auth login                                        # interactive
jk auth login --host https://ci.co --user me --token abc  # non-interactive
jk auth login --host https://ci.co --user me --token abc --alias prod
```

---

## `jk auth status`

Show authentication status.

```
jk auth status [--host HOST]
```

Exits 0 if valid, 1 if invalid.

```
Host:  https://jenkins.company.com
User:  jane
Auth:  valid
```

---

## `jk list`

List Jenkins jobs.

```
jk list [--folder FOLDER] [-r|--recursive]
```

| Flag | Description |
|------|-------------|
| `--folder` | Folder path to list jobs within |
| `-r, --recursive` | List jobs recursively across all folders |

Table columns: NAME, STATUS, LAST BUILD. Folders shown with trailing `/`.

```bash
jk list                          # all top-level jobs
jk list --folder team/frontend   # jobs in folder
jk list -r                       # all jobs recursively
jk list --json                   # JSON output
jk list --format '{{range .}}{{.Name}}{{"\n"}}{{end}}'
```

---

## `jk status`

Show build status.

```
jk status [job] [build#] [--limit N]
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
jk status my-app            # last 10 builds
jk status my-app --limit 3  # last 3 builds
jk status my-app 47         # build detail
jk status my-app 47 --json  # JSON detail
```

---

## `jk run`

Trigger a build.

```
jk run [job] [-p KEY=VALUE]... [--wait] [--log]
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
jk run my-app                              # fire and forget
jk run my-app -p BRANCH=main -p ENV=prod   # with parameters
jk run my-app --wait                       # wait for result
jk run my-app --wait --log                 # wait + stream log
jk run --log                               # auto-detect job
```

---

## `jk log`

View build console output.

```
jk log [job] [build#] [-f|--follow] [--stage STAGE] [--grep PATTERN] [--tail N] [--head N]
```

| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow live output |
| `--stage` | Show log for a specific pipeline stage |
| `--grep` | Filter log lines matching pattern |
| `-i, --ignore-case` | Case-insensitive `--grep` matching |
| `--tail N` | Show only the last N lines |
| `--head N` | Show only the first N lines |

- Defaults to latest build if no build# given
- Auto-follows if build is in progress (disabled when `--grep`, `--tail`, or `--head` active)
- `--stage` requires Blue Ocean plugin
- `--tail` and `--head` are incompatible with `--follow`

```bash
jk log my-app                    # latest build, full log
jk log my-app 42                 # specific build
jk log my-app -f                 # follow live
jk log my-app --stage Build      # specific stage log
jk log my-app --grep ERROR       # filter lines
jk log my-app --grep error -i    # case-insensitive filter
jk log my-app --tail 50          # last 50 lines
jk log my-app --head 20          # first 20 lines
```

---

## `jk open`

Open Jenkins page in browser.

```
jk open [job] [build#]
```

- No build# opens the job page
- With build# opens the build page
- Accepts full Jenkins URLs directly
- Works on Linux (`xdg-open`), macOS (`open`), Windows (`rundll32`)

```bash
jk open                  # current job (from .jk.yml or context)
jk open my-app           # job page
jk open my-app 47        # build page
```

---

## `jk abort`

Abort a running build.

```
jk abort [job] [build#] [-w|--wait]
```

| Flag | Description |
|------|-------------|
| `-w, --wait` | Wait until the build actually stops |

Defaults to the latest build. Checks if the build is running before sending the stop signal.

```bash
jk abort my-app              # abort latest build
jk abort my-app 42           # abort specific build
jk abort my-app 42 --wait    # abort and wait for it to stop
```

---

## `jk rebuild`

Retrigger a build with the same parameters.

```
jk rebuild [job] [build#] [--wait] [--log]
```

| Flag | Description |
|------|-------------|
| `--wait` | Wait for build to complete |
| `--log` | Stream build log (implies `--wait`) |

Defaults to the latest build. Retrieves the source build's parameters and triggers a new build with the same values. Exit codes match `jk run --wait`.

```bash
jk rebuild my-app 42            # retrigger build 42
jk rebuild my-app 42 --wait     # retrigger and wait
jk rebuild my-app 42 --log      # retrigger and stream log
```

---

## `jk test`

Show test results.

```
jk test [job] [build#] [--failed] [--new-failures]
```

| Flag | Description |
|------|-------------|
| `--failed` | Show only failed tests |
| `--new-failures` | Show only tests that regressed from the previous build |

Defaults to the latest build. Displays test cases with class, name, status, and duration. Failed tests include error details. Summary shows pass/fail/skip counts.

```bash
jk test my-app                   # all test results
jk test my-app 42                # specific build
jk test my-app 42 --failed       # only failures
jk test my-app --new-failures    # regressions from previous build
```

---

## `jk artifacts`

List or download build artifacts.

```
jk artifacts [job] [build#] [-d FILENAME] [-o PATH]
```

| Flag | Description |
|------|-------------|
| `-d, --download` | Download artifact by filename |
| `-o, --output` | Output file path (default: original filename in cwd) |

Defaults to the latest build. Without `-d`, lists artifacts as a table.

```bash
jk artifacts my-app 42                          # list artifacts
jk artifacts my-app 42 -d report.xml            # download to ./report.xml
jk artifacts my-app 42 -d report.xml -o /tmp/   # download to /tmp/report.xml
```

---

## `jk changes`

Show SCM changes in a build.

```
jk changes [job] [build#]
```

Defaults to the latest build. Displays commit hash (7 chars), author, and message.

```bash
jk changes my-app               # latest build changes
jk changes my-app 42            # specific build
jk changes my-app --json        # JSON output
```

---

## `jk diagnose`

Analyze a failed build and show failure summary.

```
jk diagnose [job] [build#]
```

Fetches build metadata, identifies failed stages, extracts error lines, and shows commits and parameters. Defaults to the latest build. Accepts full Jenkins URLs.

```bash
jk diagnose my-app 42
jk diagnose https://jenkins.example.com/job/team/job/svc/42/
jk diagnose my-app --json
```

---

## `jk diff`

Compare two builds of the same job.

```
jk diff [job] [build1] [build2]
```

Shows differences in parameters, stage outcomes, test results, and commits between two builds.

- Two build numbers: compares them directly
- One build number: compares with the previous build
- No build numbers: compares the latest two builds

```bash
jk diff my-app 41 42        # compare builds 41 and 42
jk diff my-app 42           # compare build 42 with build 41
jk diff my-app              # compare latest two builds
```

---

## `jk queue`

Show pending builds in the queue.

```
jk queue [--job FILTER]
```

| Flag | Description |
|------|-------------|
| `--job` | Filter queue items by job name (substring match) |

Displays queue ID, job name, and reason for each pending build.

```bash
jk queue                    # all queued builds
jk queue --job my-app       # filter by job name
jk queue --json             # JSON output
```

### `jk queue cancel`

Cancel a queued build.

```
jk queue cancel <queue-id>
```

```bash
jk queue cancel 12345
```

---

## `jk config`

Manage CLI configuration.

### `jk config list`

Show all configured hosts with user, alias, and default status.

```bash
jk config list
```

### `jk config set-default`

Set the default Jenkins host.

```bash
jk config set-default https://jenkins.prod.com
jk config set-default prod    # by alias
```

### `jk config remove`

Remove a configured host.

```bash
jk config remove https://jenkins.staging.com
jk config remove staging    # by alias
```

### `jk config set-alias`

Set or change an alias for a host.

```bash
jk config set-alias https://jenkins.prod.com prod
```

---

## `jk lint`

Validate a declarative Jenkinsfile against Jenkins. Scripted pipelines (`node { }`) are not supported.

```
jk lint [file]
```

- Defaults to `./Jenkinsfile` if no file given
- Uses Jenkins pipeline-model-converter validate endpoint
- Exit 0 if valid, exit 1 with error details if invalid

```bash
jk lint                         # validate ./Jenkinsfile
jk lint path/to/Jenkinsfile     # validate specific file
```

---

## `jk completion`

Generate shell completion script.

```
jk completion [bash|zsh|fish|powershell]
```

```bash
jk completion bash >> ~/.bashrc
jk completion zsh >> ~/.zshrc
jk completion fish > ~/.config/fish/completions/jk.fish
jk completion powershell >> $PROFILE
```
