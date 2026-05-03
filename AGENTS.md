# Agent Instructions

Guidance for AI coding agents working on this repo.

## Before you start

1. Read [README.md](README.md) for the user-facing pitch and command surface.
2. Read [CONTRIBUTING.md](CONTRIBUTING.md) for build/test layout and the
   "Adding a Command" recipe.
3. Read [docs/DESIGN.md](docs/DESIGN.md) for architecture, error model, and
   output conventions.

## Conventions

- `cmd/` is Cobra plumbing only. Business logic lives in `internal/`.
- HTTP lives in `internal/api/`. Domain types in `internal/jenkins/types.go`.
  Errors in `internal/jenkins/errors.go` (return typed errors — see DESIGN.md
  table).
- stderr is for progress/warnings, stdout is for data. Respect `--json`,
  `--format`, `NO_COLOR`.
- Don't add features beyond what the task requires. No premature abstraction.

## Quality gates before declaring work done

```bash
go vet ./...
go test ./... -count=1
gofmt -l .                 # must print nothing
make integration-test      # if behavior change
```

If you change command output format, also update `docs/commands.md`.

## Commit style

Short, imperative, no emoji. Focus on what changed, not how.
