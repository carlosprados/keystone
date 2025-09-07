# Repository Guidelines

## Project Structure & Module Organization
- Root module: `github.com/carlosprados/keystone` (Go).
- Preferred layout:
  - `cmd/keystone/`: main entrypoint binary.
  - `internal/…`: non-public packages (e.g., orchestrator, runtime, store).
  - `pkg/…`: optional public packages intended for reuse.
  - `configs/`: example config files and templates.
  - `test/`: integration or e2e helpers.
  - `scripts/`: dev utilities (format, lint, release).

## Build, Test, and Development Commands
- Build: `go build ./...` — compiles all packages.
- Run (example): `go run ./cmd/keystone` — runs the primary binary.
- Unit tests: `go test ./...` — executes all tests.
- Coverage: `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out`.
- Vet: `go vet ./...` — static checks for common issues.

## Coding Style & Naming Conventions
- Formatting: use `gofmt` (or `go fmt ./...`) before committing.
- Linting: prefer `go vet`; `golangci-lint run` if configured locally.
- Indentation: tabs (default Go tooling); wrap lines thoughtfully.
- Packages: short, lower-case names without underscores (e.g., `runtime`, `scheduler`).
- Exported identifiers: `CamelCase`; unexported: `camelCase`.
- Files: tests end with `_test.go`; examples with `_example_test.go`.

## Testing Guidelines
- Framework: standard `testing` package with table-driven tests.
- Scope: unit tests near code; integration tests under `test/`.
- Coverage target: ≥80% for core orchestration logic.
- Naming: `TestXxx`, `BenchmarkXxx`, and `_test.go` files colocated with sources.
- Running specific tests: `go test ./internal/scheduler -run TestReconcile`.

## Commit & Pull Request Guidelines
- Commits: use Conventional Commits (e.g., `feat: add reconciler loop`, `fix: handle process restart`).
- PRs: include summary, rationale, linked issues, and test evidence (logs or coverage). Add repro steps for bug fixes.
- Keep PRs focused and under ~300 LOC when possible.

## Security & Configuration Tips
- Run with least privilege; avoid requiring root in examples.
- Validate and sanitize all external inputs (configs, RPC, env).
- Document sensitive config keys under `configs/` with safe defaults.

## Keystone Agent Overview

- Purpose: manage edge components with a lightweight, process-first model. Containers are optional later via a separate runner.
- Core responsibilities:
  - Supervisor: lifecycle FSM per component (install/start/health/stop)
  - Deployment engine: DAG resolution, checkpoints, rollback
  - Artifact manager: download, verify (sha256/signature), cache, GC
  - Local API: health, component listing, basic actions
  - Observability: structured logs, Prometheus metrics
- Non-goals for MVP: remote control-plane, container runtime integration, secret backends (will arrive incrementally).

## Development Notes

- Keep the MVP stdlib-only (no network-needed dependencies) to ensure builds in restricted environments. Add third-party libs later behind clear interfaces.
- Prefer simple, deterministic code paths over clever abstractions; the agent must be easy to reason about during incidents.
- Public surface area lives under `pkg/` only when stability is intended; everything else stays under `internal/`.
- Recipes use TOML for human editing. Treat recipe files as read-only inputs and store runtime state separately.

## Contributing

- Use Conventional Commits (feat, fix, docs, refactor, chore).
- Add unit tests near the code you change. Favor table-driven tests.
- Run: `go build ./...`, `go vet ./...`, and format with `go fmt ./...`.
- Keep PRs focused and small (ideally <300 LOC).
