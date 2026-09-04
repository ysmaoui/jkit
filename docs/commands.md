# Command Reference

## Global Flags

All commands support these flags:

| Flag | Description |
|------|-------------|
| `--host HOST` | Override Jenkins host URL |
| `--branch NAME` | Branch of a multibranch pipeline job (e.g. `feature/x`); slashes are encoded for you |
| `--json` | Output as JSON |
| `--format TMPL` | Output using Go template |
| `--no-color` | Disable colored output |
| `--verbose` | Show HTTP request/response details |
| `--timeout DUR` | HTTP client timeout (default `30s`) |
| `--pipeline-source SRC` | Pipeline backend: `auto` (default), `pgv`, `blueocean` (env `JKIT_PIPELINE_SOURCE`) |

---

## Job Targets

Commands documented below as `[job] [build#]` also take a full Jenkins URL in
place of the job path. The build number may sit in the URL or follow it as a
separate argument; passing both with different numbers is an error.

```bash
jkit status my-app 47
jkit status https://jenkins.company.com/job/my-app/47/
jkit status https://jenkins.company.com/job/my-app/ 47
```

Omitting the job path falls back to `.jkit.yml`, then the git remote, then the
directory name. Omitting the build number uses the latest build.

A multibranch pipeline job holds one child job per branch, and only those
children have builds. Name the branch with `--branch`, or paste a URL, which
already carries it. Targeting the parent without a branch returns an error
listing the branches available.

```bash
jkit log team/svc 42 --branch feature/x
jkit log https://jenkins.company.com/job/team/job/svc/job/feature%2Fx/42/
```

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

## `jkit search`

Find jobs across the instance by name. Walks every folder (recursively) and
prints the full paths of jobs whose name matches a case-insensitive substring.

```
jkit search <pattern> [--folder FOLDER] [--limit N]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--folder` | Limit the search to a folder subtree | (whole instance) |
| `--limit` | Maximum results (0 = no limit); omitted matches are noted on stderr | 0 |

Table columns: JOB (full path), TYPE (`pipeline`, `multibranch`, `freestyle`, …), STATUS.

```bash
jkit search backend                # match anywhere in the instance
jkit search my-svc --folder team   # scope to a subtree
jkit search deploy --limit 50      # cap results
jkit search api --json             # JSON output
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

## `jkit params`

List the build parameters a job accepts — so you know what to pass to
`jkit run -p KEY=VALUE` without opening the Jenkins UI.

```
jkit params [job]
```

Table columns: NAME, TYPE, DEFAULT, CHOICES, DESCRIPTION. `TYPE` is the
friendly kind (`string`, `choice`, `boolean`, `password`, `text`, …).
Password defaults are masked. A job that is not parameterized prints a notice.

```bash
jkit params my-app                 # list parameters
jkit params team/backend/my-svc    # nested job
jkit params my-app --json          # JSON output
```

---

## `jkit inspect`

Read a job's `config.xml` and report which Jenkinsfile runs, the repository and
credentials behind it, which branches and PRs the indexing discovers, when a
discovered head actually builds, and how long builds are kept.

```
jkit inspect [job] [--xml [-o FILE | --recursive -d DIR]] [--history [--show-system]] [--diff [--diff-from TS --diff-to TS]] [--show-secrets]
```

| Flag | Description |
|------|-------------|
| `--xml` | Print the raw `config.xml` instead of the decoded summary |
| `-o, --output FILE` | With `--xml`, write to this file instead of stdout |
| `--recursive` | With `--xml`, export `config.xml` for every job and folder below the target |
| `-d, --out-dir DIR` | With `--xml --recursive`, the directory to write the exported tree into |
| `--show-secrets` | Do not mask credentials embedded in SCM urls |
| `--history` | List config changes (who changed the job and when) instead of its definition |
| `--show-system` | With `--history`, list automated SYSTEM writes instead of collapsing them |
| `--diff` | Show what changed in the `config.xml` between two recorded revisions |
| `--diff-from TS` | With `--diff`, the older revision, as a `--history` timestamp (`2006-01-02_15-04-05`) |
| `--diff-to TS` | With `--diff`, the newer revision, as a `--history` timestamp |

`--show-system` requires `--history`, `--diff-from` and `--diff-to` require
`--diff`, `--recursive` and `-d` require `--xml` and each other, and
`--show-secrets` applies only to the definition view, so combining it with
either of the others is rejected rather than silently ignored. `-o` and
`--recursive` are mutually exclusive: one file cannot hold a tree.

`--xml` is the escape hatch: it prints exactly what Jenkins stores, for fields
the decoder does not model and for migrating a job to code. It reformats and
redacts nothing, so a credential embedded in an SCM url appears in full. The
four modes are mutually exclusive.

`/api/json` answers none of this: it reports a multibranch job's `sources` as
empty objects. Reading `config.xml` needs the Job/ExtendedRead permission.

A job normally references a `credentialsId`, but an SCM url can carry the secret
inline as `https://user:token@host/repo.git`. Those are printed as
`https://***@host/repo.git` in text, `--json` and `--format` alike.

Each section is printed only when the job has it. Numeric strategy ids are
translated into the wording of the Jenkins UI. Anything the CLI cannot decode is
printed by class name and marked `not decoded`, prefixed with `!`, and a section
that is absent gets a line saying what its absence means, because "no discovery
traits" and "no build strategies" both change which branches build. On a branch
child the discovery rules live on the multibranch parent, and the output names
the parent to inspect instead.

```
Job:          team/my-service
Type:         multibranch pipeline
State:        enabled

Repository
  Provider:           GitHub
  Repo:               ACME/my-service

Discovery (which heads indexing picks up)
  Branch discovery     all branches  [strategyId=3]
! Clone option         not decoded, class jenkins.plugins.git.traits.CloneOptionTrait
```

### `--xml --recursive`: export a whole folder subtree

```
jkit inspect SANDBOXES --xml --recursive -d ./configs
```

Writes the target's `config.xml` and one for every job and folder below it, into
a directory tree that mirrors the folder layout. This is the bulk form of the
migration case that is otherwise scripted with `curl` over a hand-kept list of
job paths.

```
configs/
├── config.xml                      # SANDBOXES itself
└── Gecko-vemb/
    ├── config.xml                  # the multibranch job
    └── feature%2Fbuild/
        └── config.xml              # its branch child
```

Each directory is named by the job **name**, verbatim. A multibranch branch job
is where that matters: Jenkins stores the branch `feature/build` as a job whose
name literally contains `%2F`, so the encoded form is the real name. Written
as-is it stays one directory entry and feeds straight back in
(`jkit inspect SANDBOXES/Gecko-vemb/feature%2Fbuild`); decoding it into nested
directories would change the shape of the tree and make it indistinguishable
from a folder `feature` holding a job `build`.

Job names are user data, and this turns them into filesystem paths. A name that
is not a single directory entry — `..`, or one carrying a path separator — is
refused, along with everything below it, rather than rewritten into something
that no longer maps back to the job.

Per-job failures do not stop the export: an unreadable job is recorded and the
rest still runs, so a permission gap on one job out of twenty-five costs one
file rather than all of them. The summary names the counts and the destination,
and an export that skipped anything exits non-zero.

```
Wrote 26 config.xml files (412839 bytes) to ./configs
They are unredacted, exactly as Jenkins stores them, so any credential embedded in an SCM url is now on disk there.

Skipped 1:
  SANDBOXES/private-app: cannot read config.xml for SANDBOXES/private-app: reading a job's configuration needs the Job/ExtendedRead permission
```

The traversal is the one behind `jkit list -r` and carries its limit: the tree
query reaches 5 levels below the target, and anything deeper is absent from the
response with no marker.

### `--history`: who changed the job, and when

`jkit inspect my-app --history` answers "it worked last week, what changed?" by
listing the job's configuration changes, newest first, from the
[JobConfigHistory](https://plugins.jenkins.io/jobConfigHistory/) plugin.

```
WHEN                                       OPERATION  USER                DETAIL
2026-08-27 14:58:12 → 2026-08-27 14:58:13  2 writes   SYSTEM              automated, collapsed
2026-08-20 10:00:00                        Changed    Ada Lovelace (ada)  bumped the timeout
2026-08-14 14:58:12 → 2026-08-14 14:58:13  2 writes   SYSTEM              automated, collapsed

4 of 5 entries are folded into the runs above, all repeated automated writes (re-indexing rewrites a branch job's config on every scan); --show-system lists them one by one.

Retained entries: 5. The plugin caps how many it keeps per job and the server can truncate the response without saying so, so this is what it still holds, not every change ever made.
```

Re-indexing rewrites a branch job's config on every scan, so on a multibranch
branch nearly every entry is a SYSTEM write. Consecutive runs of two or more
that say the same thing are folded into one row; `--show-system` lists them
individually. A run breaks whenever the operation changes or an entry carries a
rename or a change reason, so a SYSTEM `Created` keeps its own row rather than
disappearing into a range labelled as scan churn.

The `USER` column shows the login alongside the display name, because the
display name is self-editable in Jenkins and two people can share one.

The entry count is what the server still retains, not a change count: the plugin
caps entries per job (`maxHistoryEntries`) and the instance-wide
`maxEntriesPerPage` truncates the response with no marker. An empty result is
also ambiguous — without Job/Configure or Job/ExtendedRead the plugin returns an
empty list rather than refusing, so "nothing changed" and "you may not see it"
look the same, and the message says so.

`operation` is display text the plugin resolved from its message bundle when it
wrote the entry, so a controller running in another locale stores `変更` instead
of `Changed`. It is printed as stored and nothing keys on it; SYSTEM writes are
recognised by `userID`.

On a Jenkins without the plugin, `/jobConfigHistory` 404s with exactly the page a
missing job produces. `jkit` re-requests the job itself to tell the two apart and
names the plugin rather than claiming the job does not exist.

`--json` and `--format` emit every entry, uncollapsed.

### `--diff`: what the change actually did

`--history` says a revision exists; `--diff` says what is in it. It fetches two
stored revisions of the `config.xml` and prints a unified diff of them.

```
jkit inspect my-app --diff
jkit inspect my-app --diff --diff-from 2026-07-24_13-06-30 --diff-to 2026-08-27_14-58-13
```

With no timestamps it compares the two most recent revisions, which is the
"what changed since yesterday?" case. The timestamps are the `date` field of a
history entry, exactly as `--json` on `--history` prints it, so the usual route
is to list first and copy a pair across.

```
--- team/my-service @ 2026-07-24_13-06-30
+++ team/my-service @ 2026-08-27_14-58-13
@@ -12,7 +12,7 @@
   <scriptPath>Jenkinsfile</scriptPath>
-  <numToKeep>10</numToKeep>
+  <numToKeep>50</numToKeep>
```

The older revision is always the left side, whichever way round the two
timestamps are given, because the plugin's own diff view orders them that way
and two tools disagreeing about the direction of the same change is worse than
one of them ignoring an argument.

Only the diff itself goes to stdout, so it pipes into `patch`, `diffstat` or a
pager unchanged. Everything else, including "these two revisions are identical",
is written to stderr.

Two warnings the output carries for you:

- A timestamp the plugin does not hold is answered with HTTP 200 and an empty
  body, not a 404, so `jkit` treats an empty response as the failure and names
  the timestamp that caused it. A timestamp that is not in the plugin's format
  is rejected before the request.
- With Job/ExtendedRead but not Job/Configure, Jenkins masks secrets on the way
  out and re-encrypts stored ones on every save. Two revisions of a job nobody
  touched can therefore differ. When every change in the diff is such a value,
  the output says so instead of letting it read as a configuration change.

`--diff` needs two revisions. A job the plugin has recorded once, which is the
normal state of a folder, is reported as having nothing to compare against.

`--json` and `--format` emit the same diff as hunks: `from`, `to`, `maskedOnly`
and a `hunks` array of `oldStart`/`oldLines`/`newStart`/`newLines` with the
lines and their `op` (`" "`, `-`, `+`).

```bash
jkit inspect my-app                              # structured view
jkit inspect team/backend/my-svc                 # nested job
jkit inspect my-app --branch feature/foo         # a branch of a multibranch job
jkit inspect https://jenkins.example.com/job/x/  # by URL
jkit inspect my-app --json                       # JSON output
jkit inspect my-app --history                    # who changed the job config, and when
jkit inspect my-app --history --show-system      # including automated re-index writes
jkit inspect my-app --diff                       # what changed in the last config edit
jkit inspect my-app --diff --diff-from 2026-07-24_13-06-30 --diff-to 2026-08-27_14-58-13
```

---

## `jkit history`

Show a job's recent builds with a trend summary: success rate over the window
and how the latest build's duration compares to the median.

```
jkit history [job] [--limit N]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--limit` | Number of recent builds to include | 20 |

Table columns: #, RESULT, DURATION, STARTED, CAUSE. A summary line follows:

```
#   RESULT   DURATION  STARTED       CAUSE
48  SUCCESS  2m10s     Jan 02 15:04  Started by user Yacine
47  FAILURE  1m02s     Jan 02 14:00  Started by GitHub push

Success rate: 8/10 (80%)   Median duration: 2m05s   Last vs median: +4%
```

In-progress and zero-duration builds are excluded from the duration median.

```bash
jkit history my-app             # last 20 builds + trend
jkit history my-app --limit 50  # wider window
jkit history my-app --json      # JSON output
```

---

## `jkit env`

Dump the environment variables injected into a build (via the EnvInject plugin's
`/injectedEnvVars` endpoint) — useful for answering "why did this build behave
differently". When build# is omitted, the last build is used.

```
jkit env [job] [build#] [--filter SUBSTR] [--show-secrets]
```

| Flag | Description |
|------|-------------|
| `--filter` | Only show vars whose name contains this substring (case-insensitive) |
| `--show-secrets` | Do not mask secret-looking values |

Output is `KEY=VALUE`, sorted by name. Secret-looking values (`PASSWORD`,
`TOKEN`, `SECRET`, …) are masked by default. Requires the EnvInject plugin on
the Jenkins server; a clear error is shown if it is absent.

```bash
jkit env my-app                 # last build
jkit env my-app 42              # specific build
jkit env my-app 42 --filter GIT # only GIT* vars
jkit env my-app 42 --json       # JSON output
```

---

## `jkit run`

Trigger a build.

```
jkit run [job] [-p KEY=VALUE]... [--wait] [--log]
```

| Flag | Description |
|------|-------------|
| `-p, --param KEY=VALUE` | Build parameter (repeatable) |
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
