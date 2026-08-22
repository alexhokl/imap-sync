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
to `--state-file` (default `./sync-state.json`) after each folder, so a
re-run will skip messages already synchronised.

## Docker Compose

```yaml
services:
  imap-sync:
    image: asia-east1-docker.pkg.dev/craffy/public/imap-sync:<TAG>
    environment:
      IMAP_PORT: 143
      IMAPS_PORT: 993
      TZ: "UTC"
      IMAP_SYNC_SOURCE_HOST: <SOURCE_HOST>
      IMAP_SYNC_SOURCE_USER: <SOURCE_USER>
      IMAP_SYNC_SOURCE_PASS: <SOURCE_PASS>
      IMAP_SYNC_DEST_HOST: <DEST_HOST>
      IMAP_SYNC_DEST_USER: <DEST_USER>
      IMAP_SYNC_DEST_PASS: <DEST_PASS>
      IMAP_SYNC_STATE_FILE: /data/sync-state.json
      # Required for the self-signed cert from gen-certs.sh.
      # Keep trailing comments off this line.
      IMAP_SYNC_DEST_SKIP_TLS: true
    volumes:
      - ./sync-state.json:/data/sync-state.json
```

```sh
docker compose up
```

The state file is mounted as a volume so dedup state survives container
restarts.
