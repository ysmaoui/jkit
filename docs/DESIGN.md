# Design Notes — `jkit`: A Developer-First Jenkins CLI

> Architectural and design context for `jkit`. For user docs see [README.md](../README.md)
> and [docs/quickstart.md](quickstart.md). For contributor onboarding see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Project Vision

`jkit` is a command-line interface for Jenkins, inspired by GitHub's `gh` CLI. Where `gh` transformed GitHub from a browser-first experience into a terminal-native workflow, `jkit` does the same for Jenkins. The core insight is that developers interact with Jenkins in predictable, repetitive patterns — checking build status, reading logs, triggering builds, approving inputs — and all of these should be fast, scriptable, and zero-config from the terminal.

This is NOT an admin tool. This is NOT a wrapper around the Jenkins REST API. This is a developer productivity tool that thinks in workflows, not endpoints.

---

## Technology Stack

- **Language:** Go 1.26+
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
│   ├── input.go               # jkit input (list / --approve / --deny)
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
func (c *Client) TriggerBuild(jobPath string, params map[string]string) (*TriggerResult, error)
func (c *Client) ConsoleLog(jobPath string, number int) *ProgressiveLog
func (c *Client) GetPipelineStages(jobPath string, number int) ([]Stage, error)
func (c *Client) GetPendingInputs(jobPath string, number int) ([]PendingInput, error)
func (c *Client) ApproveInput(jobPath string, number int, in PendingInput, params map[string]string) error
func (c *Client) DenyInput(jobPath string, number int, in PendingInput) error
```

### Jenkins API Patterns

Important Jenkins REST API patterns to implement:

```
# Job info (JSON API — append /api/json to any Jenkins URL)
GET /job/{path}/api/json?tree=name,url,color,lastBuild[number,result,timestamp]

# Build info
GET /job/{path}/{number}/api/json?tree=number,result,timestamp,duration,building

# Trigger build (requires crumb)
POST /job/{path}/build                          # job defines no parameters
POST /job/{path}/buildWithParameters             # job defines parameters; omitted ones take defaults

# Console log (supports progressive fetching). Where an answer's text stops
# depends on the Stapler version; see "progressiveText offsets" below.
GET /job/{path}/{number}/logText/progressiveText?start={byte-offset}
# Response headers: X-Text-Size, X-More-Data (set while the log is written)
# The whole console as it stands: notes stripped, line ends as stored, an
# unfinished last line included. No offset and no size header.
GET /job/{path}/{number}/consoleText
# Jenkins version (X-Jenkins). Jenkins sends the header on api/json and pages
# but not on progressiveText, so it is asked here once when no answer has
# carried it yet.
GET /api/json?tree=_class

# Pipeline stages — Pipeline Graph View (preferred, plugin v803+)
GET /job/{path}/{number}/stages/tree
# Returns: { status, data: { complete, stages: [ {id, name, state, type,
#           pauseDurationMillis, startTimeMillis, totalDurationMillis,
#           children[], isSequential, synthetic, placeholder, agent, url } ] } }

# Per-line console timestamps (Timestamper plugin, not core)
GET /job/{path}/{number}/timestamps/?elapsed=HH:mm:ss.S&startLine=-N&appendLog
GET /job/{path}/{number}/timestamps/?time=HH:mm:ss&startLine=1&endLine=N&appendLog
GET /job/{path}/{number}/timestamps/?elapsed=SSSSS          # times only, no log body
# Indexed by LINE, not byte: startLine is 1-based and may be negative to count
# back from the end; endLine is inclusive. No X-More-Data and no X-Text-Size, so
# nothing here can drive a live tail or a size guard. See the note below.

# Pipeline stage / step log (single endpoint, accepts stage IDs and step IDs)
# Takes no start offset and sends no X-Text-Size; always the whole log from
# byte 0. Blue Ocean's nodes/{id}/log/ is the same. Tails read past the head
# client-side, and so does a follow that cannot use the step routes below.
GET /job/{path}/{number}/stages/log?nodeId={id}
# stages/log writes, for each step of a stage in this listing's order, the
# step's log and then its exception text. `log --stage -f` rebuilds that from
# the two routes below plus the core step log route, reading only new bytes.
GET /job/{path}/{number}/stages/steps?nodeId={id}
# Returns: { status, data: { runIsComplete, steps: [ {id, name, state, type,
#           title, stageId, pauseDurationMillis, startTimeMillis,
#           totalDurationMillis} ] } }, sorted by ID. state as in stages/tree.
GET /job/{path}/{number}/stages/exceptionText?nodeId={stepId}
# Plain text; empty for a step without an error. Only fetched for a step in
# state failure or aborted, which is where an ErrorAction lives.

# One step's log (core + workflow-support, no plugin). progressiveText from a
# start= offset, read as the console is (see "progressiveText offsets"). A step
# that has written nothing has no LogAction yet and 404s. On Jenkins 2.509 to
# 2.533, a step that passes the 10000-line cap while running is shown to its
# end once it completes; if the stage ends first, -f fails naming the step.
GET /job/{path}/{number}/execution/node/{stepId}/log/logText/progressiveText?start={byte-offset}
# The flow node page. Probed once, on the first step log 404, to tell a step
# without a log from a server where this route is missing or blocked.
GET /job/{path}/{number}/execution/node/{id}/

# Pipeline stages — Blue Ocean (fallback for instances without PGV ≥ 803)
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/{nodeId}/log/
# Blue Ocean answers node log with 500 on some parallel containers. The steps
# below are the fallback: list the stage's steps, then read each step's log in
# order, one at a time, through the same stage log cap. A step log without
# start= is only its last 150 KB (LogResource.DEFAULT_LOG_THRESHOLD), so start=0
# is always sent. The concatenation is not append-only, so it cannot be followed;
# -f lists these steps and reads each through the core step log route instead.
# The listing pages with start= and limit= (@PagedResponse, default limit 100),
# read until a page comes back empty since a short page may be a clamped limit.
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/{nodeId}/steps/?start={n}&limit=10000
GET /blue/rest/organizations/jenkins/pipelines/{path}/runs/{number}/nodes/{nodeId}/steps/{stepId}/log/?start=0

# Abort a running build (requires crumb)
POST /job/{path}/{number}/stop
# Answers a redirect, not the resulting state. Re-read the build to confirm.

# Run state (requires crumb; Job/Configure)
POST /job/{path}/enable
POST /job/{path}/disable
# The route exists only for a job type whose supportsMakeDisabled is true, which
# Jenkins defaults to false, so a 404 here means the type cannot be toggled
# rather than that the job is missing. Like /stop it redirects rather than
# reporting the new state, so the state is re-read afterwards. NOTE: these two
# paths are built from a variable, so the catalogue test cannot see them —
# keep them here by hand.

# Build environment (EnvInject plugin, not core)
GET /job/{path}/{number}/injectedEnvVars/api/json
# Exists only when a job actually used EnvInject, which pipeline jobs never do,
# so a 404 is the normal case and not evidence the plugin is absent. The
# pipeline run's own EnvActionImpl is read from /api/json?tree=actions[environment]
# instead, and carries only what the script assigned to env.*

# Test results (junit plugin, not core)
GET /job/{path}/{number}/testReport/api/json
# A build that published no tests 404s, which is a normal answer meaning "no
# report", not an error.

# Artifacts
GET /job/{path}/{number}/api/json?tree=artifacts[fileName,relativePath]
GET /job/{path}/{number}/artifact/{relativePath}   # the bytes, streamed

# Pending input steps (core exported beans; see the note below)
GET /job/{path}/{number}/api/json?tree=actions[_class,waitingForInput,
#     executions[id,settled,input[message,ok,submitter,submitterParameter,
#     parameters[_class,name,type,description,defaultParameterValue[value],choices]]]]

# Settle an input step (requires crumb)
POST /job/{path}/{number}/input/{inputId}/proceedEmpty  # approve, no parameters
POST /job/{path}/{number}/input/{inputId}/proceed       # approve, json=<submitted form>
POST /job/{path}/{number}/input/{inputId}/abort         # deny

# CSRF crumb
GET /crumbIssuer/api/json

# Queue
GET /queue/api/json?tree=items[id,task[name,url],why,inQueueSince]
GET /queue/item/{id}/api/json   # poll one item; `executable` appears once it starts
POST /queue/cancelItem?id={id}  # requires crumb

# Jenkinsfile validation (linting)
POST /pipeline-model-converter/validate  # body: jenkinsfile=<contents>

# Job definition (the only source; see the note below)
GET /job/{path}/config.xml               # needs Job/ExtendedRead

# Config change history (JobConfigHistory plugin, not core)
GET /job/{path}/jobConfigHistory/api/json
GET /job/{path}/jobConfigHistory/configOutput?type=raw&timestamp=2006-01-02_15-04-05

# Branch indexing (branch-api; containers only, no api/json exists)
GET /job/{path}/indexing/consoleText                        # multibranch project
GET /job/{path}/indexing/logText/progressiveText?start=N
GET /job/{path}/computation/logText/progressiveText?start=N # organization folder
```

**Reading a job's definition.** `/api/json` cannot substitute for `config.xml`.
On a multibranch job it returns `"sources": [{}]`, and `GitHubRepoMetadataAction`,
`ObjectMetadataAction` and `ViewCredentialsAction` all come back with no exported
fields, so every SCM fact is reachable only through the XML. Jenkins writes it
with an XML 1.1 declaration, which Go's `encoding/xml` rejects outright, and it
may contain control characters that are legal in 1.1 but not 1.0.

**Two traps in JobConfigHistory.** A Jenkins without the plugin answers
`/jobConfigHistory` with a 404 whose body is identical to a missing job, so the
404 path probes the job itself before choosing which failure to report. And
The `/timestamps/` response is counted in log lines, but its BODY is not. A
console line that carried carriage returns — a `curl` progress meter is the usual
source — is stored with embedded newlines, and the plugin prefixes only its first
fragment. Asking for lines 649-651 on one real build returns five newline-
separated lines. Anything that bounds output by counting what comes back will
therefore stop short; send the bound to the server as `endLine` instead. The same
line numbering does match `logText/progressiveText` exactly, so a line number
from one is valid in the other.

Timestamper's "prepend to console" mode writes `[2026-09-04T15:07:35.462Z]` into
the text that `progressiveText` serves. `/timestamps/?appendLog` serves the
unannotated log, so the two do not stack and output is never double-stamped.

`configOutput` answers a bad, missing or malformed timestamp with HTTP 200 and a
zero-byte body, so the status code carries no information and an empty body is
the error signal. The `operation` field is a localized display string resolved at
write time, not an enum, so nothing keys off its English text.

**Branch indexing has no structured form, only a log.** `/indexing/api/json` is a
404 on a multibranch job whose `/indexing/consoleText` returns 200, so the record
of what a scan did is English prose written by the SCM source plugin. GitHub,
Bitbucket and plain git word the same verdicts differently, which makes any
branch-to-verdict table a heuristic. `jkit scan` therefore prints the log by
default, extracts a `Checking branch X` block verbatim for `--branch`, and marks
`--summary` best-effort: it names the wording it was built from, prints verbatim
every line it cannot classify, and compares the log's own "N branches were
processed" totals against the blocks it parsed. A multibranch project publishes
the run as `/indexing`; an organization folder is a plain `ComputedFolder` and
publishes it as `/computation` (both answer on a multibranch job, and the class
decides which is used). Jenkins keeps only the last run, so the scan's start time
is read off line two and its age is reported: without that, "your branch is not
in the log" reads as "rejected" when it usually means "not looked at yet".

The plugin's own diff endpoint (`diffFiles`/`showDiffFiles`) is deliberately
unused: it returns Jelly HTML, its `api/json` route is a Jenkins 404 page, and
`getLines()` carries no `@Exported`. jkit fetches two revisions and diffs them
locally.

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

**Input steps do not use `/wfapi`, and that is not an oversight.**
`pipeline-rest-api` does expose `/wfapi/pendingInputActions`, but its
`PendingInputActionsExt` carries no `submitter` field, so a refusal read through
it could not name who is allowed to answer the step — which is the whole
difference between a useful error and "permission denied". The core route has
what it needs: `InputAction` is an `@ExportedBean` with `@Exported`
`waitingForInput` and `executions`, and `InputStep` exports `message`, `ok`,
`submitter`, `submitterParameter` and `parameters`. It also drops a dependency:
the POST endpoints that settle an input come from `pipeline-input-step`, so
reading through the same plugin means input support needs exactly one plugin
rather than two, and generation 1 stays unused.

The tree query names `_class` deliberately. Jenkins answers a tree query
containing field names that do not exist with HTTP 200 and silently omits them —
verified against a live instance, where `?tree=number,thisFieldDoesNotExistAtAll`
returned the build with only `number`. So no field's absence proves anything,
and `_class` is the exception: Jenkins always emits it for an exported action,
and renders actions with no exported properties as `{}`. Finding no InputAction
is therefore a positive answer ("this build never reached an input step"), not
an inference from silence.

`/proceed` is used for a parameterised approval rather than `/submit`, because
`doSubmit` ABORTS the input whenever the request carries no `proceed` field: a
malformed approval would turn into a denial. `/proceedEmpty` approves a step
that declares parameters using their DEFAULTS, so `internal/api` refuses to call
it for such a step unless the caller supplied every value.

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

`jkit log -f`, `run --log`, `rebuild --log` and `scan -f` poll
`progressiveText` once a second through `api.ProgressiveLog` and stop once the
log is complete. `Ctrl+C` stops following without killing the process
ungracefully.

#### progressiveText offsets

Checked against Jenkins 2.479.3 and 2.568.3 and Stapler's `LargeText`. The
offset is in stored bytes, console notes included.

| Answer | Text | Where the text stops |
|---|---|---|
| Log complete, any version | the rest of the log | X-Text-Size, or meta `end` |
| Multipart (`Accept: multipart/form-data`, Stapler 2050+, Jenkins 2.534+) | a `text` part written through core's `PlainTextConsoleOutputStream`: notes stripped, stored line ends, the unfinished last line held back; then `meta` `{completed, start, end}` | unknown while running: `end` is the stored length, the unfinished line included |
| Plain, running, Jenkins up to 2.508 | notes kept, lone LF rewritten to CRLF, up to the last CR or LF and at most 10000 of them (Stapler's `TailMark` counts each) | X-Text-Size, counted from what was sent |
| Plain, running, Jenkins 2.509+ (or a proxy dropping `Accept`) | as above, but from 2.527 (Stapler #703) cut after X-Text-Size stored bytes when the log grew during the answer: mid-line, even mid-character | unknown: X-Text-Size is the stored length |

The multipart mode also takes a negative `start` (the tail from its first line
start, `meta.startFromNewLine`) and `searchNewLineUntil`; all three came in the
same Stapler change (#722).

Two consequences shape the reader:

- Bytes received never give the offset. Plain bodies go through
  `LineEndNormalizingWriter`, which turns a lone LF into CRLF but leaves a
  stored CRLF alone, so the two cannot be told apart; multipart text has notes
  stripped. Advancing by bytes received overshoots, and Stapler answers a start
  past the end from 0, so a follow repeats the console (27 times for a
  12000-line one on 2.568.3).
- Trusting the multipart `end` or an uncounted X-Text-Size skips an unfinished
  last line, and on 2.509-2.533 anything past the line cap.

`ProgressiveLog` keeps an anchor, a stored offset it has written the text up
to, and how much text past the anchor it has written. An exact answer moves
the anchor to its end. Otherwise the stored length an answer reported is a
candidate when its text ran to the last line end before it (no line cap, and
a plain body that cannot have run past that length). The written text then
stops right after the last LF before the candidate, and the next poll moves
the anchor there. The multipart case relies on the text part holding back the
unterminated last line, as core's `AnnotatedLargeText` does by writing it
through the line-based `PlainTextConsoleOutputStream`; a server sending that
line would have it written twice.

- Multipart: the LF is searched for with `searchNewLineUntil`. Asked from x
  with `searchNewLineUntil=candidate+1`, Stapler's `findNextLineStart` reads
  the stored bytes from x up to the candidate, console notes included, and
  answers from the line start after the first LF there (`meta.start`,
  `startFromNewLine`), or from x when there is none. Every answer carries the
  text from its start to the end of the log, so the asks are kept few: the
  byte before the candidate first while the last search found the LF there
  (a log written in whole lines, as a pipeline step's, ends in one at every
  poll); then where the LF would be if the stored bytes were the text
  written, and the byte after (a log found mid-line with no note or
  undecodable byte since the anchor); otherwise, as notes put the LF later
  and bytes Jenkins decodes to U+FFFD earlier, strides doubling down from
  the candidate, then halving. A found answer starts after the first LF at
  or after where it was asked from, which moves the lower bound to that LF.
  The answer from the last LF is written.
- Plain: one ask from the byte before the candidate. Plain text keeps notes,
  so it begins with a newline exactly when the stored byte is one.

When the anchor cannot move, the poll asks from it again and writes only the
text past what it wrote. On 2.534+ a poll that lands on a line end takes one
request holding only what is new, and one that lands mid-line two, each
carrying what is new. With console notes or undecodable bytes since the
anchor, the search takes several requests, each again carrying everything
new from where it starts, so a note-heavy log found mid-line can download
the new text several times over. On 2.509-2.533 polls that keep landing
mid-line re-read from the last line start a poll landed on. A multipart
answer held for its meta part past 16 MB is dropped and the anchor stays for
that poll.

A plain body ending in a CR is not written past the CR: only the next byte
says whether it is half of a CRLF, and asked from the byte after it Stapler
would turn that LF into CRLF too. Such an answer sets no candidate, and a
counted one puts the anchor on the CR. The plain body's CR before LF is
dropped, so a CRLF the build wrote prints as LF there.

On 2.509-2.533 a running log that passes the line cap past the anchor cannot
move it: every answer from the anchor stops at the same line. The cap counts
a CRLF received as two line ends, since it may be a stored CRLF, so a log of
LF lines is taken as capped from 5000 lines. The reader then only checks
X-More-Data until the log completes, and says so once on stderr.

Stapler 1979 to 2028 (Jenkins 2.509 to 2.526, LTS 2.516) also read on while the
log grows during an answer, so a plain body can run past X-Text-Size. That is
no line cap: the answer is written, sets no candidate, and the next poll asks
from the anchor again. The stored length of a body cannot be told exactly (a
CRLF received may be a stored LF), so on those versions, or an unknown one, an
answer's size is a candidate only when a request right after it finds the log
still that size. The anchor thus moves only when the log does not grow across
an answer's whole transfer and one more round trip; a chatty build is re-read
from the anchor every poll and stalls at the line cap.

From 2.527 the body instead stops after X-Text-Size stored bytes, so when the
log grew during the answer it can end mid-line, its last character decoded
from part of its bytes as U+FFFD. A running plain answer is therefore written
only up to its last CR or LF; the rest is not counted as written, and the next
poll reads that line again from the anchor, whole.

A multipart answer that shows a start before the one asked for (or other than
it, without `searchNewLineUntil`), or a plain one with X-Text-Size below it, is
the log resent from 0 and fails the read. Logs only grow, so this is not
expected.

A poll that finds the log ending inside a console note sees part of the note
as text in neither mode: plain answers stop at the last line end before it,
and the multipart text holds back the unterminated line. The note is stripped
or kept whole once its line completes.

Non-follow readers:

- `--grep`, `--head`, the unfiltered dump and `sources` stream `consoleText`,
  which is exact on every version and needs no offset. A running build is read
  as it stands and the command exits.
- `--tail` and `diagnose` read a byte window up to the last line end, doubled
  while it holds fewer lines than asked for:
  - 2.534+: one multipart request with `start=-window`.
  - Up to 2.508: X-Text-Size, then pages forward by counted offsets keeping
    only the window. A running log's X-Text-Size from 0 counts only its first
    10000 lines, so the pages can cover most of the log; memory stays bounded.
  - 2.509-2.533: X-Text-Size, then one answer from the window. A window past
    the line cap is halved until it fits; when that cannot hold the lines asked
    for, the whole `consoleText` is read, keeping only the tail.
- `--max-bytes` compares X-Text-Size, which on a running log before 2.509 is a
  lower bound.

A read is one HTTP response, bounded in memory by streaming, but it has to
arrive within `--timeout`, which covers reading the body; a timed-out console
read says to raise it. It cannot be cut into chunks: the stored offset at a
cut is unknown for the reasons above, and chunks advanced by bytes received
skip text at every cut (67215 lines of a 12 MB console). An idle-read timeout
in place of the whole-response one would lift the limit, but would change what
`--timeout` means for every command; it is not done.

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
- `jkit input <job> [build#] [--approve|--deny]` — input step interaction (implemented)
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
- **Optional plugins:** Blue Ocean (needed for `jkit stages` and stage-level logs — gracefully degrade without it); `pipeline-input-step` (needed for `jkit input`, and installed wherever the `input` step is used at all)
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

**The permission line is relaxed; the mutation line is not.** Commands that need
more than a normal build user's rights are acceptable, so a job owner or release
engineer is a supported user. Writing a job's definition is still out: `config.xml`
writes, job creation and seed-job pushes belong in jobDSL, not a CLI. `jkit inspect
--xml` reads the file; nothing writes it. Replay is the useful boundary case, and
it clears the line because it runs an edited script once and persists nothing.

**A refusal must name what is missing.** Once commands need different rights, a
failure stops being unambiguous, and the three causes look identical from the
outside: you lack a permission, a plugin is absent, or the thing does not exist.
Every command that can be refused must distinguish them, naming the permission
and the object it is needed on.

This is not hypothetical. `jkit env` used to answer every pipeline build with
"the EnvInject plugin is not installed" on instances where it *was* installed:
`/injectedEnvVars` only exists when a job actually used EnvInject, which pipeline
jobs never do. The 404 was the normal case, and the message blamed the wrong
thing. Getting this wrong scales with every elevated command added.

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

