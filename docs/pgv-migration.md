# Pipeline Graph View migration plan

## Context

Blue Ocean is deprecated; PGV is the maintained replacement. Target: migrate
pipeline stage + log retrieval from `/blue/rest/...` to PGV `/stages/...` with
Blue Ocean as fallback for instances still on PGV < 803.

Plugin prerequisite: **PGV v803+** (URL_NAME `stages`). Validated against a
representative real-world pipeline build on a Jenkins LTS instance.

## PGV API (validated)

- `GET {buildUrl}/stages/tree` — `{status, data:{complete, stages:[{id,name,state,type,title,pauseDurationMillis,startTimeMillis,totalDurationMillis,children[],isSequential,synthetic,placeholder,agent,url}]}}`
- `GET {buildUrl}/stages/log?nodeId={id}` — `text/plain`; accepts **both stage IDs and step IDs** (single endpoint — step-level aggregation no longer needed)
- `GET {buildUrl}/stages/steps?nodeId={stageId}` — steps list (optional, not required for core flows)

State values (lowercase): `success, failure, unstable, aborted, not_built, skipped, running, paused, queued, unknown`.
Types: `STAGE, PARALLEL, PARALLEL_BLOCK` (explicit container type — no heuristic fan-out filtering needed).

## Validation findings (2026-04-20)

| Concern | Result |
|---|---|
| Tree endpoint | OK, 178 stages, nested `children[]`, depth 2 |
| Log endpoint | OK for leaves, branches, and step IDs |
| Step-level fallback | NOT NEEDED — `/stages/log` takes stepId too |
| Nested parallel | Explicit `PARALLEL_BLOCK` wrapper + `PARALLEL` branches |
| State mapping | lowercase → uppercase straightforward |
| `not_built` | ⚠️ silently returned as `success` in tested build — prefer `result` from Blue Ocean when available, or cross-check `synthetic`/`placeholder` |
| **Latency** | ⚠️ ~11× slower than Blue Ocean (3.3s vs 0.29s). Payload smaller (80 KB vs 207 KB) — server-side processing dominates |
| Node ID parity | Leaf/branch IDs match Blue Ocean; synthetic wrappers differ (PGV uses real FlowNode IDs) |

## Migration strategy

**Try-PGV-then-fallback.** PGV first; Blue Ocean on 404 or when PGV response is
malformed. Emit a single request per call — no dual-fetch. Users on older PGV
stay on Blue Ocean transparently.

Opt-out env var `JK_PIPELINE_SOURCE=auto|pgv|blueocean` (default `auto`) so
latency-sensitive users can pin Blue Ocean until PGV performance improves.

## Tasks

### Task 1: Add PGV types
- **Type:** task
- **Priority:** P1
- **Description:** Add `PGVResponse`, `PGVData`, `PGVStage` (recursive `Children []PGVStage`) and `PGVStep` to `internal/jenkins/types.go`. Fields per validated API above. Keep existing `Stage` struct unchanged so downstream code stays stable.
- **Acceptance:** Types compile; unit test decodes captured sample payload into nested tree.
- **Files:** `internal/jenkins/types.go`, new `internal/jenkins/types_pgv_test.go`

### Task 2: State/type mapping helper
- **Type:** task
- **Priority:** P1
- **Description:** Add `mapPGVState(string) string` mapping lowercase PGV states to Blue-Ocean-style uppercase values consumed by `Stage.Status`: `success→SUCCESS, failure→FAILURE, unstable→UNSTABLE, aborted→ABORTED, not_built→NOT_BUILT, skipped→NOT_BUILT, running→IN_PROGRESS, paused→PAUSED_PENDING_INPUT, queued→QUEUED, unknown→""`. Document `not_built` ambiguity — PGV occasionally emits `success` for skipped stages; not resolvable client-side.
- **Acceptance:** Table-driven unit test covers all documented states + empty fallback.
- **Files:** `internal/jenkins/stage_tree.go` (or new `internal/jenkins/pgv.go`)
- **Deps:** Task 1

### Task 3: FlattenPGVTree
- **Type:** task
- **Priority:** P1
- **Description:** Add `FlattenPGVTree([]PGVStage) []Stage` — DFS walk, populate `FirstParent` from parent ID, convert durations, map state via Task 2 helper, copy `Type`. Drop `PARALLEL_BLOCK` aggregator nodes (empty logs, no real work) to match current `NonContainerStages` behavior. Preserve `isSequential` via new `Stage.IsSequential bool` field if display layer needs it — otherwise skip.
- **Acceptance:** Unit test: feed saved PGV payload, assert resulting `[]Stage` matches Blue Ocean `/nodes/` shape for leaf stages (same IDs, durations, parent chain).
- **Files:** `internal/jenkins/stage_tree.go`, `internal/jenkins/stage_tree_test.go`
- **Deps:** Tasks 1, 2

### Task 4: GetPipelineStages — PGV-first with fallback
- **Type:** task
- **Priority:** P1
- **Description:** Refactor `GetPipelineStages` in `internal/api/pipeline.go`: build classic `/job/...` URL via `NormalizeJobPath`, GET `/stages/tree`. On 200: decode + `FlattenPGVTree`. On 404 (old PGV or missing plugin): fall through to existing Blue Ocean path. Respect env `JK_PIPELINE_SOURCE`: `pgv` → no fallback (return error), `blueocean` → skip PGV entirely, `auto`/unset → current default.
- **Acceptance:** Existing `diagnose_test.go` / pipeline tests pass. New test with httptest mocks both endpoints and asserts PGV preferred, fallback on 404.
- **Files:** `internal/api/pipeline.go`, `internal/api/diagnose_test.go`, new test
- **Deps:** Task 3

### Task 5: GetStageLog — PGV-first with fallback
- **Type:** task
- **Priority:** P1
- **Description:** Refactor `GetStageLog` in `internal/api/pipeline.go`: GET `/stages/log?nodeId={id}` first. On 404/500: fall back to Blue Ocean `/blue/rest/.../nodes/{id}/log/`. Since PGV `/stages/log` already serves step-level IDs, **delete `getStageLogViaSteps`** once PGV path is in place — the step-aggregation fallback only existed because Blue Ocean's node-log returned 500 on parallel containers; PGV doesn't need it.
- **Acceptance:** Existing callers unaffected. Integration run against validated build returns log for leaf stage, parallel branch, and unstable stage.
- **Files:** `internal/api/pipeline.go`
- **Deps:** Task 4

### Task 6: Source selector env var + --pipeline-source flag
- **Type:** feature
- **Priority:** P2
- **Description:** Read `JK_PIPELINE_SOURCE` in client init; accept `--pipeline-source=auto|pgv|blueocean` on commands that hit pipeline APIs (`diagnose`, `status`, `log`). Passes through to `GetPipelineStages` / `GetStageLog`. Default `auto` (PGV-first). Document in README.
- **Acceptance:** `JK_PIPELINE_SOURCE=blueocean jk diagnose ...` only hits `/blue/rest/`; `pgv` only hits `/stages/`; `auto` tries PGV then Blue.
- **Files:** `cmd/diagnose.go`, `cmd/status.go`, `cmd/log.go`, `internal/api/client.go`
- **Deps:** Tasks 4, 5

### Task 7: Integration smoke + README
- **Type:** chore
- **Priority:** P2
- **Description:** Run `jk diagnose` and `jk status` against the validated build on all three modes (`auto`, `pgv`, `blueocean`); record timings and any output deltas. Update README section on supported plugins.
- **Acceptance:** Three runs succeed, output matches within state-mapping tolerances, timings noted.
- **Deps:** Task 6

## Verification

E2E against a real pipeline build with nested parallel stages:

1. `JK_PIPELINE_SOURCE=pgv jk diagnose <job> <build#>`
2. `JK_PIPELINE_SOURCE=blueocean jk diagnose ...` — same command
3. `JK_PIPELINE_SOURCE=auto jk diagnose ...` — default path
4. `jk status ... <build#> --stages` — verify nested parallel rendering
5. `jk log ... <build#> --stage <nodeId>` — leaf, parallel branch, `PARALLEL_BLOCK` (should yield empty or fall back cleanly)

## Unresolved questions

- Should `PARALLEL_BLOCK` containers be kept (for accurate total-duration display) or dropped (current Blue Ocean behavior via `NonContainerStages`)? Recommendation: drop, to preserve existing UX.
- `not_built` mis-reporting as `success` — accept as known PGV limitation, or add targeted Blue Ocean cross-check when a stage has `synthetic=true`? Recommendation: accept; document.
- Latency: PGV 11× slower on the benchmark build. Acceptable for `diagnose` (one-shot). For interactive commands (`status --watch`), consider caching tree within a single invocation. Revisit after real usage.
- Older PGV versions (< 803): return 404 on `/stages/tree`, so fallback triggers automatically — no version probe needed.

## Sources

- [OpenAPI spec](https://github.com/jenkinsci/pipeline-graph-view-plugin/blob/main/openapi.yaml)
- [PipelineConsoleViewAction.java](https://github.com/jenkinsci/pipeline-graph-view-plugin/blob/main/src/main/java/io/jenkins/plugins/pipelinegraphview/consoleview/PipelineConsoleViewAction.java)
- [RestClient.tsx](https://github.com/jenkinsci/pipeline-graph-view-plugin/blob/main/src/main/frontend/common/RestClient.tsx)
