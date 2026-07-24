# AGENTS.md

## Project

`megadl` is a Go terminal UI for listing and downloading public MEGA links.
It uses Bubble Tea for the UI, SQLite for history/quota state, and a native
MEGA protocol implementation (no external downloader).

## Layout

- `main.go`: configuration, database, engine, and TUI wiring.
- `internal/mega`: small driver/event interfaces shared by the engine and tests.
- `internal/meganet`: MEGA link parsing, API/crypto, folder traversal, and resumable downloads.
- `internal/engine`: single-active-download queue, stop/resume, and quota accounting.
- `internal/db`: SQLite schema, migrations, and persistence.
- `internal/ui`: Bubble Tea models and views.
- `internal/config`, `internal/naming`: configuration and safe destination naming.

## Working conventions

- Format changed Go files with `gofmt`.
- Run `go test ./...` before handing off; tests use temp directories and local
  fake servers, so they should not need network access.
- Use `go test ./internal/<package> -run TestName` for focused iteration.
- After making changes, rebuild the app with `go build -o megadl .`. Avoid
  launching it during automated checks: it creates user configuration/state
  and may contact MEGA.
- Keep network/process details behind `mega.Driver` and `mega.Proc` so engine
  behavior remains testable with fakes.
- Preserve download invariants: only one active queue item, transfer quota
  counts newly received bytes only, and `.megatmp.<handle>` partials remain
  resumable until MAC verification succeeds.
- Keep Bubble Tea `Update` paths non-blocking; perform I/O in `tea.Cmd`s.
- Prefer ANSI terminal colors over hex color literals in UI styling.
- SQLite schema changes must include a migration path for existing databases
  and retain foreign-key behavior.
- Add or update focused tests alongside behavior changes, especially around
  resume, cancellation, selection, quota, crypto, and path sanitization.
- Do not add tests for simple cosmetic changes where a test would provide
  little practical value.

## Important notes

- Do not worry about backwards compatibility; this is a personal project.
