package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// dialDestFn is the function used by syncFolder to connect to the destination.
// It is a package-level variable so tests can replace it with a plain-TCP
// dialer to reach the in-process imapmemserver without TLS.
var dialDestFn = DialDest

// dialSourceFn is the function used by runAccount to connect to a source
// account's server. It is a package-level variable so tests can replace it
// with a plain-TCP dialer to reach the in-process imapmemserver without TLS.
var dialSourceFn = DialSource

// Config holds the settings needed to sync one source account's folders to
// its paired destination login. It is derived per-account from AppConfig.
type Config struct {
	DestHost    string
	DestUser    string
	DestPass    string
	DestSkipTLS bool

	StateFile string
	DryRun    bool
}

func main() {
	appCfg := parseConfig()

	log.SetFlags(log.Ltime)
	log.Printf("imap-sync starting")
	log.Printf("dest host: %s (skip-tls: %v)", appCfg.Dest.Host, appCfg.Dest.SkipTLS)
	log.Printf("accounts:  %d", len(appCfg.Accounts))
	if appCfg.DryRun {
		log.Printf("DRY RUN — no messages will be synchronised")
	}

	var totalNew, totalSkipped int
	for i := range appCfg.Accounts {
		acc := &appCfg.Accounts[i]
		cfg := &Config{
			DestHost:    appCfg.Dest.Host,
			DestUser:    acc.DestUser,
			DestPass:    acc.DestPass,
			DestSkipTLS: appCfg.Dest.SkipTLS,
			StateFile:   acc.StateFile,
			DryRun:      appCfg.DryRun,
		}

		newCount, skippedCount, err := runAccount(cfg, acc)
		if err != nil {
			log.Printf("[%s] ERROR: %v (continuing with next account)", acc.Name, err)
			continue
		}
		totalNew += newCount
		totalSkipped += skippedCount
	}

	log.Printf("done — %d synchronised, %d skipped (already synced) across %d account(s)",
		totalNew, totalSkipped, len(appCfg.Accounts))
}

// runAccount syncs every folder of one source account to its paired
// destination login. Errors from an individual folder are logged and do not
// abort the rest of the account's folders; state is saved after each folder
// that delivered new messages.
func runAccount(cfg *Config, acc *AccountConfig) (totalNew, totalSkipped int, err error) {
	log.Printf("[%s] source: %s (user: %s)", acc.Name, acc.SourceHost, acc.SourceUser)
	log.Printf("[%s] state:  %s", acc.Name, cfg.StateFile)

	state, err := LoadState(cfg.StateFile)
	if err != nil {
		return 0, 0, fmt.Errorf("load state: %w", err)
	}

	log.Printf("[%s] connecting to source...", acc.Name)
	src, err := dialSourceFn(acc.SourceHost, acc.SourceUser, acc.SourcePass)
	if err != nil {
		return 0, 0, fmt.Errorf("source: %w", err)
	}
	defer func() { _ = src.Close() }()

	folders, err := src.ListFolders()
	if err != nil {
		return 0, 0, fmt.Errorf("list folders: %w", err)
	}
	log.Printf("[%s] found %d folder(s) on source", acc.Name, len(folders))

	if cfg.DryRun {
		log.Printf("[%s] dry-run: testing connection to destination...", acc.Name)
		if err := testDestConnection(cfg); err != nil {
			return 0, 0, fmt.Errorf("dest: %w", err)
		}
		log.Printf("[%s] dry-run: destination connection OK", acc.Name)
	}

	srcDelim := src.Delimiter()
	var destDelim rune // fetched once, on the first per-account dest connection

	for _, folder := range folders {
		newCount, skippedCount, ferr := syncFolder(cfg, src, state, folder, srcDelim, &destDelim)
		if ferr != nil {
			log.Printf("[%s] ERROR syncing %q: %v (continuing)", acc.Name, folder, ferr)
			continue
		}
		totalNew += newCount
		totalSkipped += skippedCount

		// Save state after each folder so progress is not lost on interruption.
		if !cfg.DryRun && newCount > 0 {
			if err := state.Save(cfg.StateFile); err != nil {
				return totalNew, totalSkipped, fmt.Errorf("save state after %q: %w", folder, err)
			}
		}
	}

	log.Printf("[%s] done — %d synchronised, %d skipped (already synced)", acc.Name, totalNew, totalSkipped)
	return totalNew, totalSkipped, nil
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

func syncFolder(cfg *Config, src *SourceClient, state State, folder string, srcDelim rune, destDelim *rune) (newCount, skippedCount int, err error) {
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
	var messageIDCount, hashCount int
	for _, m := range metas {
		if strings.HasPrefix(m.DedupKey, "sha256:") {
			hashCount++
		} else {
			messageIDCount++
		}
		if state.Has(folder, m.DedupKey) {
			skippedCount++
		} else {
			toFetch = append(toFetch, m)
		}
	}
	log.Printf("[%s] %d message(s): %d new, %d already synced",
		folder, len(metas), len(toFetch), skippedCount)
	log.Printf("[%s] dedup keys: %d from Message-Id, %d from sha256 fallback",
		folder, messageIDCount, hashCount)

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

	if *destDelim == 0 {
		d, err := dest.Delimiter()
		if err != nil {
			return 0, skippedCount, fmt.Errorf("get dest delimiter: %w", err)
		}
		*destDelim = d
		log.Printf("dest delimiter: %q", *destDelim)
	}
	destFolder := translateMailboxName(folder, srcDelim, *destDelim)

	if err := dest.EnsureFolder(destFolder); err != nil {
		return 0, skippedCount, fmt.Errorf("ensure folder %q on dest: %w", folder, err)
	}

	for i, meta := range toFetch {
		msg, err := src.FetchFull(meta.UID)
		if err != nil {
			log.Printf("[%s] warning: fetch UID %d: %v — skipping", folder, meta.UID, err)
			continue
		}

		if err := dest.Append(destFolder, msg); err != nil {
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

// parseConfig reads flags, falling back to IMAP_SYNC_* env vars, and returns
// the resolved multi-account AppConfig. If --config (or IMAP_SYNC_CONFIG) is
// set, accounts are loaded from that YAML file. Otherwise a single implicit
// account is built from the legacy --source-*/--dest-*/--state-file flags,
// preserving prior single-account behavior exactly.
func parseConfig() *AppConfig {
	var configPath string
	var sourceHost, sourceUser, sourcePass string
	var destHost, destUser, destPass string
	var destSkipTLS bool
	var stateFile string
	var dryRun bool

	flag.StringVar(&configPath, "config", envOr("IMAP_SYNC_CONFIG", ""), "Path to multi-account YAML config file")

	flag.StringVar(&sourceHost, "source-host", envOr("IMAP_SYNC_SOURCE_HOST", ""), "Source IMAP host:port")
	flag.StringVar(&sourceUser, "source-user", envOr("IMAP_SYNC_SOURCE_USER", ""), "Source IMAP username")
	flag.StringVar(&sourcePass, "source-pass", envOr("IMAP_SYNC_SOURCE_PASS", ""), "Source IMAP password")

	flag.StringVar(&destHost, "dest-host", envOr("IMAP_SYNC_DEST_HOST", "localhost:993"), "Destination IMAP host:port")
	flag.StringVar(&destUser, "dest-user", envOr("IMAP_SYNC_DEST_USER", ""), "Destination IMAP username")
	flag.StringVar(&destPass, "dest-pass", envOr("IMAP_SYNC_DEST_PASS", ""), "Destination IMAP password")
	flag.BoolVar(&destSkipTLS, "dest-skip-tls", envBool("IMAP_SYNC_DEST_SKIP_TLS", false), "Skip TLS verification for destination (self-signed cert)")

	flag.StringVar(&stateFile, "state-file", envOr("IMAP_SYNC_STATE_FILE", "/state/sync-state.json"), "Path to sync state file")
	flag.BoolVar(&dryRun, "dry-run", false, "Fetch headers and log without synchronising")

	flag.Parse()

	if configPath != "" {
		cfg, err := loadConfigFile(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		cfg.DryRun = dryRun
		return cfg
	}

	if sourceUser == "" {
		fmt.Fprintln(os.Stderr, "error: --source-user (or IMAP_SYNC_SOURCE_USER) is required")
		os.Exit(1)
	}
	if sourcePass == "" {
		fmt.Fprintln(os.Stderr, "error: --source-pass (or IMAP_SYNC_SOURCE_PASS) is required")
		os.Exit(1)
	}
	if destUser == "" {
		fmt.Fprintln(os.Stderr, "error: --dest-user (or IMAP_SYNC_DEST_USER) is required")
		os.Exit(1)
	}
	if destPass == "" {
		fmt.Fprintln(os.Stderr, "error: --dest-pass (or IMAP_SYNC_DEST_PASS) is required")
		os.Exit(1)
	}

	return &AppConfig{
		Dest: DestHostConfig{
			Host:    destHost,
			SkipTLS: destSkipTLS,
		},
		Accounts: []AccountConfig{
			{
				Name:       "",
				SourceHost: sourceHost,
				SourceUser: sourceUser,
				SourcePass: sourcePass,
				DestUser:   destUser,
				DestPass:   destPass,
				StateFile:  stateFile,
			},
		},
		DryRun: dryRun,
	}
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
