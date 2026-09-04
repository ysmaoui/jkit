# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed
- A build number passed as a positional argument alongside a Jenkins URL is no
  longer ignored: `jkit status <job-url> 42` now targets build 42 instead of
  listing recent builds. Applies to every command that accepts a URL target,
  including `jkit open`. A number given in both the URL and the argument must
  match, otherwise the command errors.

### Changed
- `docs/scripting.md` examples read `building` before `result`. Jenkins serves a
  result on in-progress builds, so `.result` alone can report SUCCESS on a build
  that is still running.

## [0.6.0] - 2026-07-23

### Added
- `jkit params [job]` — list the build parameters a job accepts (name, type,
  default, choices), so you know what to pass to `jkit run -p` without opening
  the Jenkins UI. Password defaults are masked. Read-only.
- `jkit search <pattern>` — find jobs across the instance by case-insensitive
  name match, with `--folder` to scope a subtree and `--limit` to cap results.
- `jkit env [job] [build#]` — dump a build's injected environment variables (via
  the EnvInject `/injectedEnvVars` endpoint). Secret-looking values are masked by
  default (`--show-secrets` to reveal, `--filter` to narrow); a clear error is
  shown when the plugin is absent.
- `jkit history [job]` — recent builds with a success-rate and duration-trend
  summary (median duration, last-vs-median delta).

### Fixed
- README no longer advertises "approve inputs from your terminal" — there is no
  such command.

## [0.5.0] - 2026-06-15

### Added
- `--branch <name>` flag for multibranch pipeline jobs. The `org/name build#`
  shorthand has no place for a branch, and branch names with slashes (e.g.
  `feature/foo/bar`) can't be expressed positionally. `--branch` encodes the
  branch as a single job segment, so `jkit log SANDBOX/app 6 --branch feature/foo`
  resolves the same path as the full classic URL.

### Fixed
- `jkit diagnose` no longer reports `Duration: < 1s` for an in-progress build.
  Jenkins reports `duration=0` while building; diagnose now shows elapsed time
  (`now - start`) labelled `(running)`.
- Requesting a build on a multibranch pipeline or folder container (which have
  no builds of their own) now returns an error that names the container and lists
  its branches/child jobs, instead of a bare 404 ("run 'jkit list'") or a
  misleading "blue ocean plugin required". Applies to `log`, `diagnose`,
  `status`, `stages`, and other build commands.

## [0.4.0] - 2026-06-05

### Fixed
- Large completed-build consoles are no longer silently truncated. `GetBuildLog`
  previously read only the first 10 MB chunk and reported the build's full size
  as the next offset, so any console over 10 MB stopped after one chunk. It now
  pages on bytes actually read, so `jkit log`, `--grep`, and `diagnose` see the
  whole log.
- `jkit diagnose` console fallback now scans the tail (where failures surface)
  instead of the head.

### Added
- `jkit log --max-bytes N` — refuse to dump an unfiltered console larger than N
  bytes (default 50 MB; `0` = unlimited) rather than streaming a huge log.
- Windowed/streaming log handling: `--tail` fetches only a server-side tail
  window, `--head` stops reading early, and `--grep` streams the full log with
  bounded memory (early exit under `--head`).

### Changed
- Stage-log reads emit a stderr warning when they hit the 10 MB cap, so
  truncation is never silent.

## [0.3.0] - 2026-05-29

### Added
- `jkit stages` command — list pipeline stages with node IDs and qualified
  paths that disambiguate duplicate names across parallel branches.
- `jkit log --stage-id` — select a stage by exact node ID.
- `jkit log --stage` accepts qualified paths (e.g. `"RemoteExec/Run Bazel Build"`)
  and can be combined with `-f` to tail a single stage of a running build.

### Changed
- `jkit log --stage` now errors and lists candidates when a bare name matches
  multiple stages, instead of silently picking one.

### Dependencies
- Bump `golang.org/x/term` from 0.42.0 to 0.43.0.

## [0.1.0] - 2026-05-03

- Initial public release.
