# imap-sync

A CLI application pull from one mailbox of IMAP server to another

## Local

Build and run the binary directly on bare metal:

```sh
task build
./imap-sync \
  --source-host <SOURCE_HOST> \
  --source-user <SOURCE_USER> \
  --source-pass <SOURCE_PASS> \
  --dest-host <DEST_HOST> \
  --dest-user <DEST_USER> \
  --dest-pass <DEST_PASS> \
  --dry-run
```

Drop `--dry-run` once you're happy with the plan. Sync progress is persisted
to `--state-file` (default `/state/sync-state.json`) after each folder, so a
re-run will skip messages already synchronised.

## Multiple accounts

To back up more than one source account into a shared destination server,
use `--config` (or `IMAP_SYNC_CONFIG`) pointing at a YAML file instead of the
`--source-*`/`--dest-*` flags:

```yaml
dest:
  host: <DEST_HOST>
  skip_tls: false

accounts:
  - name: alice
    source:
      host: <SOURCE_HOST>
      user: alice@example.com
      pass: <ALICE_SOURCE_PASS>
    dest:
      user: alice-backup
      pass: <ALICE_DEST_PASS>
    # state_file: /state/sync-state-alice.json   # optional, defaults to this
  - name: bob
    source:
      host: <SOURCE_HOST>
      user: bob@example.com
      pass: <BOB_SOURCE_PASS>
    dest:
      user: bob-backup
      pass: <BOB_DEST_PASS>
```

```sh
./imap-sync --config accounts.yaml --dry-run
```

- `dest.host`/`dest.skip_tls` are shared by every account (one destination
  server).
- Each account under `accounts` pairs its own source mailbox with its own
  destination login on that shared server — since each account authenticates
  to the destination as a different user, their folders never collide.
- Each account gets its own dedup state file (`state_file`, defaulting to
  `/state/sync-state-<name>.json`), so progress tracking is fully isolated
  per account. The parent directory (`/state` by default) is created
  automatically if it doesn't already exist.
- Accounts are synced sequentially; if one account fails (bad credentials,
  unreachable host, etc.) the error is logged and the remaining accounts
  still run.
- The config file contains plaintext credentials — keep it out of version
  control and restrict its file permissions (e.g. `chmod 600`).
- `--dry-run` still applies across every account in the config.

When `--config` is not set, the original single-account `--source-*`/
`--dest-*` flags and `IMAP_SYNC_*` env vars behave exactly as before.

## Docker Compose

```yaml
services:
  imap-sync:
    image: asia-east1-docker.pkg.dev/craffy/public/imap-sync:<TAG>
    environment:
      IMAP_SYNC_SOURCE_HOST: <SOURCE_HOST>
      IMAP_SYNC_SOURCE_USER: <SOURCE_USER>
      IMAP_SYNC_SOURCE_PASS: <SOURCE_PASS>
      IMAP_SYNC_DEST_HOST: <DEST_HOST>
      IMAP_SYNC_DEST_USER: <DEST_USER>
      IMAP_SYNC_DEST_PASS: <DEST_PASS>
      IMAP_SYNC_STATE_FILE: /state/sync-state.json
      # Required for the self-signed cert from gen-certs.sh.
      # Keep trailing comments off this line.
      IMAP_SYNC_DEST_SKIP_TLS: true
    volumes:
      - ./sync-state.json:/state/sync-state.json
```

```sh
docker compose up
```

The state file is mounted as a volume so dedup state survives container
restarts.

