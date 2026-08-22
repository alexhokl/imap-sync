# AGENTS.md

Go CLI (`github.com/alexhokl/imap-sync`) that pulls messages from one IMAP
mailbox into another, deduplicating via a local state file.

## Commands

Use `task` (Taskfile.yml), not raw `go` where a task exists:

- `task build` — `go build -o /dev/null ./...`
- `task test` — `go test ./... -coverprofile=coverage.out -covermode=atomic`
- `task coverage` — runs tests then prints `go tool cover -func`
- `task lint` — `golangci-lint run`
- `task sec` — `gosec -exclude-generated ./...`
- Single test: `go test ./... -run TestName -v` (single package repo, no submodules)

No codegen, no migrations, no lockfile beyond `go.sum`.

## Architecture (all files at repo root, single `main` package)

- `main.go` — CLI flag/env parsing (`IMAP_SYNC_*` env vars), orchestrates
  `syncFolder` loop over source folders.
- `fetch.go` — `SourceClient` (source IMAP: list folders, fetch headers/full
  messages).
- `deliver.go` — `DestClient` (destination IMAP: ensure folder, append
  message).
- `state.go` — `State` (map of mailbox -> set of dedup keys), persisted as
  JSON to `--state-file` (default `./sync-state.json`), saved after each
  folder to avoid losing progress on interruption.
- `bufio.go` — small buffered-reader helper.
- Dedup key is derived from message headers (sha256), not IMAP UID, so
  re-syncing after a UID change doesn't create duplicates.

## Testing quirks

- Tests do **not** hit real IMAP servers. `imap_test.go` spins up an
  in-process `imapmemserver` (from `go-imap/v2/imapserver/imapmemserver`) on a
  random localhost port and dials it with `DialInsecure`/`newSourceClient`/
  `newDestClient` constructors — these test-only constructors exist
  specifically to inject a pre-dialed connection, bypassing `DialSource`/
  `DialDest` (which require real TLS).
- `main.go` has a package-level `dialDestFn` var (defaults to `DialDest`) so
  tests can swap in a plain-TCP dialer reaching the fake dest server without
  TLS.
- No external services, network, or fixtures required to run the test suite.

## Runtime behavior

- `Append` runs flags through `sanitizeFlags`, stripping `\Recent` (server-owned,
  RFC 3501 §2.3.2) and `\*`; forwarding `\Recent` makes Dovecot-style servers
  reject the whole APPEND. All other flags and the internal date pass through.
- State saves after each folder, only if that folder delivered ≥1 new message —
  a mid-run interruption loses progress only for the folder in flight.
- A folder that errors is logged; the run continues (one bad mailbox doesn't
  abort the sync).
- `--dry-run`: per-folder "new" count reports what would be delivered; nothing
  is written, state is never saved.
- Re-running without deleting the state file is safe and fast — messages are
  header-fetched first, then skipped via dedup key before any full fetch/append.

## Build/release

- `Dockerfile` builds a `scratch`-based static binary image (no shell, no
  libc) — any new dependency must be pure Go / CGO_ENABLED=0 compatible.
- `cloudbuild.yaml` + `task tag` (auto-incrementing `0.1.x` git tags) drive
  releases; tags trigger the image build/push pipeline.

