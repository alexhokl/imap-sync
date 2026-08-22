package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
)

// dialDestFn is the function used by syncFolder to connect to the destination.
// It is a package-level variable so tests can replace it with a plain-TCP
// dialer to reach the in-process imapmemserver without TLS.
var dialDestFn = DialDest

// Config holds all runtime settings.
type Config struct {
	SourceHost string
	SourceUser string
	SourcePass string

	DestHost    string
	DestUser    string
	DestPass    string
	DestSkipTLS bool

	StateFile string
	DryRun    bool
}

func main() {
	cfg := parseConfig()

	log.SetFlags(log.Ltime)
	log.Printf("imap-sync starting")
	log.Printf("source: %s (user: %s)", cfg.SourceHost, cfg.SourceUser)
	log.Printf("dest:   %s (user: %s)", cfg.DestHost, cfg.DestUser)
	log.Printf("state:  %s", cfg.StateFile)
	if cfg.DryRun {
		log.Printf("DRY RUN — no messages will be synchronised")
	}

	state, err := LoadState(cfg.StateFile)
	if err != nil {
		log.Fatalf("load state: %v", err)
	}

	log.Printf("connecting to source...")
	src, err := DialSource(cfg.SourceHost, cfg.SourceUser, cfg.SourcePass)
	if err != nil {
		log.Fatalf("source: %v", err)
	}
	defer func() { _ = src.Close() }()

	folders, err := src.ListFolders()
	if err != nil {
		log.Fatalf("list folders: %v", err)
	}
	log.Printf("found %d folder(s) on source", len(folders))

	if cfg.DryRun {
		log.Printf("dry-run: testing connection to destination...")
		if err := testDestConnection(cfg); err != nil {
			log.Fatalf("dest: %v", err)
		}
		log.Printf("dry-run: destination connection OK")
	}

	var totalNew, totalSkipped int

	for _, folder := range folders {
		newCount, skippedCount, err := syncFolder(cfg, src, state, folder)
		if err != nil {
			log.Printf("ERROR syncing %q: %v (continuing)", folder, err)
			continue
		}
		totalNew += newCount
		totalSkipped += skippedCount

		// Save state after each folder so progress is not lost on interruption.
		if !cfg.DryRun && newCount > 0 {
			if err := state.Save(cfg.StateFile); err != nil {
				log.Fatalf("save state after %q: %v", folder, err)
			}
		}
	}

	log.Printf("done — %d synchronised, %d skipped (already synced)", totalNew, totalSkipped)
}

// testDestConnection dials and authenticates to the destination server, then
// immediately closes the connection. Used in dry-run mode to verify dest
// credentials/connectivity without performing any folder sync work.
func testDestConnection(cfg *Config) error {
	dest, err := dialDestFn(cfg.DestHost, cfg.DestUser, cfg.DestPass, cfg.DestSkipTLS)
	if err != nil {
		return err
	}
	return dest.Close()
}

func syncFolder(cfg *Config, src *SourceClient, state State, folder string) (newCount, skippedCount int, err error) {
	log.Printf("[%s] fetching headers...", folder)

	metas, err := src.FetchHeaders(folder)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch headers: %w", err)
	}
	if len(metas) == 0 {
		log.Printf("[%s] empty", folder)
		return 0, 0, nil
	}

	// Partition into new vs already-seen.
	var toFetch []MessageMeta
	for _, m := range metas {
		if state.Has(folder, m.DedupKey) {
			skippedCount++
		} else {
			toFetch = append(toFetch, m)
		}
	}
	log.Printf("[%s] %d message(s): %d new, %d already synced",
		folder, len(metas), len(toFetch), skippedCount)

	if len(toFetch) == 0 || cfg.DryRun {
		if cfg.DryRun && len(toFetch) > 0 {
			log.Printf("[%s] dry-run: would pull %d message(s)", folder, len(toFetch))
		}
		return len(toFetch), skippedCount, nil
	}

	// Dial destination only when there is work to do.
	log.Printf("[%s] connecting to dest to push %d message(s)...", folder, len(toFetch))
	dest, err := dialDestFn(cfg.DestHost, cfg.DestUser, cfg.DestPass, cfg.DestSkipTLS)
	if err != nil {
		return 0, skippedCount, fmt.Errorf("dial dest: %w", err)
	}
	defer func() { _ = dest.Close() }()

	if err := dest.EnsureFolder(folder); err != nil {
		return 0, skippedCount, fmt.Errorf("ensure folder %q on dest: %w", folder, err)
	}

	for i, meta := range toFetch {
		msg, err := src.FetchFull(meta.UID)
		if err != nil {
			log.Printf("[%s] warning: fetch UID %d: %v — skipping", folder, meta.UID, err)
			continue
		}

		if err := dest.Append(folder, msg); err != nil {
			log.Printf("[%s] warning: append UID %d: %v — skipping", folder, meta.UID, err)
			continue
		}

		state.Add(folder, meta.DedupKey)
		newCount++

		if (i+1)%50 == 0 {
			log.Printf("[%s] progress: %d/%d synchronised", folder, i+1, len(toFetch))
		}
	}

	return newCount, skippedCount, nil
}

// parseConfig reads flags, falling back to IMAP_SYNC_* env vars.
func parseConfig() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.SourceHost, "source-host", envOr("IMAP_SYNC_SOURCE_HOST", ""), "Source IMAP host:port")
	flag.StringVar(&cfg.SourceUser, "source-user", envOr("IMAP_SYNC_SOURCE_USER", ""), "Source IMAP username")
	flag.StringVar(&cfg.SourcePass, "source-pass", envOr("IMAP_SYNC_SOURCE_PASS", ""), "Source IMAP password")

	flag.StringVar(&cfg.DestHost, "dest-host", envOr("IMAP_SYNC_DEST_HOST", "localhost:993"), "Destination IMAP host:port")
	flag.StringVar(&cfg.DestUser, "dest-user", envOr("IMAP_SYNC_DEST_USER", ""), "Destination IMAP username")
	flag.StringVar(&cfg.DestPass, "dest-pass", envOr("IMAP_SYNC_DEST_PASS", ""), "Destination IMAP password")
	flag.BoolVar(&cfg.DestSkipTLS, "dest-skip-tls", envBool("IMAP_SYNC_DEST_SKIP_TLS", false), "Skip TLS verification for destination (self-signed cert)")

	flag.StringVar(&cfg.StateFile, "state-file", envOr("IMAP_SYNC_STATE_FILE", "./sync-state.json"), "Path to sync state file")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Fetch headers and log without synchronising")

	flag.Parse()

	if cfg.SourceUser == "" {
		fmt.Fprintln(os.Stderr, "error: --source-user (or IMAP_SYNC_SOURCE_USER) is required")
		os.Exit(1)
	}
	if cfg.SourcePass == "" {
		fmt.Fprintln(os.Stderr, "error: --source-pass (or IMAP_SYNC_SOURCE_PASS) is required")
		os.Exit(1)
	}
	if cfg.DestUser == "" {
		fmt.Fprintln(os.Stderr, "error: --dest-user (or IMAP_SYNC_DEST_USER) is required")
		os.Exit(1)
	}
	if cfg.DestPass == "" {
		fmt.Fprintln(os.Stderr, "error: --dest-pass (or IMAP_SYNC_DEST_PASS) is required")
		os.Exit(1)
	}

	return cfg
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
