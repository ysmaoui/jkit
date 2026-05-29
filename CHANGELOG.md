# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
