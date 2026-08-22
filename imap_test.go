package main

import (
	"bytes"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUser = "testuser"
	testPass = "testpass"
)

// literalReader adapts a []byte to imap.LiteralReader.
type literalReader struct {
	*bytes.Reader
}

func (l literalReader) Size() int64 { return int64(l.Len()) }

func newLiteralReader(b []byte) imap.LiteralReader {
	return literalReader{bytes.NewReader(b)}
}

// testMessage returns a minimal raw RFC822 message with a unique Message-ID.
func testMessage(id, subject, body string) []byte {
	return fmt.Appendf(
		nil,
		"Message-ID: <%s@test.example>\r\nDate: Thu, 1 Jan 2026 00:00:00 +0000\r\nFrom: src@example.com\r\nSubject: %s\r\n\r\n%s",
		id, subject, body,
	)
}

// ── Test server helper ────────────────────────────────────────────────────────

// newTestServer spins up an in-process imapmemserver on a random localhost port.
// It returns the bound address and the *imapmemserver.User for direct mailbox
// seeding. The server is shut down automatically via t.Cleanup.
func newTestServer(t *testing.T) (addr string, u *imapmemserver.User) {
	t.Helper()

	memSrv := imapmemserver.New()
	u = imapmemserver.NewUser(testUser, testPass)
	require.NoError(t, u.Create("INBOX", nil))
	memSrv.AddUser(u)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memSrv.NewSession(), nil, nil
		},
		InsecureAuth: true, // allow plain LOGIN over non-TLS for testing
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go srv.Serve(ln) //nolint:errcheck

	t.Cleanup(func() {
		_ = ln.Close()
		_ = srv.Close()
	})

	return ln.Addr().String(), u
}

// dialInsecure dials the test server without TLS and logs in.
func dialInsecure(t *testing.T, addr string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	require.NoError(t, err, "DialInsecure")
	require.NoError(t, c.Login(testUser, testPass).Wait(), "Login")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// appendMsg seeds a raw message directly into the in-memory user's mailbox.
func appendMsg(t *testing.T, u *imapmemserver.User, mailbox string, raw []byte, flags []imap.Flag, ts time.Time) {
	t.Helper()
	_, err := u.Append(mailbox, newLiteralReader(raw), &imap.AppendOptions{
		Flags: flags,
		Time:  ts,
	})
	require.NoError(t, err, "seed Append to %q", mailbox)
}

// ── SourceClient tests ────────────────────────────────────────────────────────

func TestSourceClient_ListFolders(t *testing.T) {
	addr, u := newTestServer(t)
	require.NoError(t, u.Create("Sent", nil))
	require.NoError(t, u.Create("Archive", nil))

	src := newSourceClient(dialInsecure(t, addr))
	folders, err := src.ListFolders()
	require.NoError(t, err)

	assert.Contains(t, folders, "INBOX")
	assert.Contains(t, folders, "Sent")
	assert.Contains(t, folders, "Archive")
}

func TestSourceClient_FetchHeaders_Empty(t *testing.T) {
	addr, _ := newTestServer(t)

	src := newSourceClient(dialInsecure(t, addr))
	metas, err := src.FetchHeaders("INBOX")
	require.NoError(t, err)
	assert.Nil(t, metas)
}

func TestSourceClient_FetchHeaders_WithMessages(t *testing.T) {
	addr, u := newTestServer(t)
	appendMsg(t, u, "INBOX", testMessage("msg-1", "First", "body one"), nil, time.Now())
	appendMsg(t, u, "INBOX", testMessage("msg-2", "Second", "body two"), nil, time.Now())

	src := newSourceClient(dialInsecure(t, addr))
	metas, err := src.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metas, 2)

	keys := make([]string, len(metas))
	for i, m := range metas {
		keys[i] = m.DedupKey
	}
	assert.Contains(t, keys, "<msg-1@test.example>")
	assert.Contains(t, keys, "<msg-2@test.example>")

	// UIDs must be non-zero.
	for _, m := range metas {
		assert.NotZero(t, m.UID)
	}
}

func TestSourceClient_FetchFull(t *testing.T) {
	addr, u := newTestServer(t)
	raw := testMessage("fetch-full-1", "Full Test", "hello world")
	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	flags := []imap.Flag{imap.FlagSeen}
	appendMsg(t, u, "INBOX", raw, flags, ts)

	src := newSourceClient(dialInsecure(t, addr))
	metas, err := src.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metas, 1)

	msg, err := src.FetchFull(metas[0].UID)
	require.NoError(t, err)
	require.NotNil(t, msg)

	assert.Contains(t, string(msg.RFC822), "hello world")
	assert.Contains(t, string(msg.RFC822), "fetch-full-1@test.example")
	assert.WithinDuration(t, ts, msg.InternalDate, time.Second)
	assert.Contains(t, msg.Flags, imap.FlagSeen)
}

// ── DestClient tests ──────────────────────────────────────────────────────────

func TestDestClient_EnsureFolder_New(t *testing.T) {
	addr, _ := newTestServer(t)
	dest := newDestClient(dialInsecure(t, addr))

	err := dest.EnsureFolder("NewFolder")
	require.NoError(t, err)
}

func TestDestClient_EnsureFolder_Existing(t *testing.T) {
	addr, _ := newTestServer(t)
	dest := newDestClient(dialInsecure(t, addr))

	// INBOX already exists — should be idempotent.
	err := dest.EnsureFolder("INBOX")
	require.NoError(t, err)
}

func TestDestClient_EnsureFolder_Idempotent(t *testing.T) {
	addr, _ := newTestServer(t)
	dest := newDestClient(dialInsecure(t, addr))

	require.NoError(t, dest.EnsureFolder("MyFolder"))
	// Second call must not error even though folder now exists.
	require.NoError(t, dest.EnsureFolder("MyFolder"))
}

func TestDestClient_Delimiter(t *testing.T) {
	addr, _ := newTestServer(t)
	dest := newDestClient(dialInsecure(t, addr))

	delim, err := dest.Delimiter()
	require.NoError(t, err)
	// imapmemserver always reports '/' as its hierarchy delimiter.
	assert.Equal(t, '/', delim)
}

func TestDestClient_Append(t *testing.T) {
	addr, _ := newTestServer(t)
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	raw := testMessage("append-1", "Append Test", "appended body")

	dest := newDestClient(dialInsecure(t, addr))
	require.NoError(t, dest.EnsureFolder("INBOX"))

	err := dest.Append("INBOX", &FullMessage{
		RFC822:       raw,
		Flags:        []imap.Flag{imap.FlagSeen},
		InternalDate: ts,
	})
	require.NoError(t, err)

	// Verify via a fresh connection.
	src := newSourceClient(dialInsecure(t, addr))
	metas, err := src.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, "<append-1@test.example>", metas[0].DedupKey)
}

// ── syncFolder tests ──────────────────────────────────────────────────────────

// newSyncConfig returns a Config pointing at the dest test server.
// The source is handled by passing a *SourceClient directly to syncFolder.
func newSyncConfig(destAddr string) *Config {
	return &Config{
		DestHost:    destAddr,
		DestUser:    testUser,
		DestPass:    testPass,
		DestSkipTLS: false,
		StateFile:   "", // not used inside syncFolder
		DryRun:      false,
	}
}

// syncFolderViaInsecureDest overrides DialDest inside syncFolder by pointing
// cfg at the plain-TCP test server (InsecureAuth, no TLS).
// We achieve this by embedding the dest test server address in cfg.DestHost
// and patching DialDest's TLS logic at the call site by setting DestSkipTLS=false
// while the server itself has InsecureAuth=true (so plain DialInsecure works).
//
// However syncFolder calls DialDest which always uses DialTLS. To allow
// in-process testing we need a way to override the dial.  We do this by
// wrapping syncFolder with a small helper that pre-creates the dest connection
// and monkey-patches it in via a closure.
//
// Simpler approach: expose a testable inner function that accepts a
// destDialer func. Because that would change production code more than
// desired, we instead use the fact that syncFolder connects to cfg.DestHost
// with plain InsecureAuth by calling imapclient.DialInsecure instead — but
// syncFolder currently always uses DialTLS via DialDest.
//
// Solution: add a package-level dialDestFn variable so tests can swap it out.

// runSyncFolder is a test-only wrapper that overrides the destination dialer
// to use InsecureAuth (plain TCP) so it can reach the in-process test server.
// srcDelim/destDelim mirror the parameters syncFolder now takes for hierarchy
// delimiter translation; pass 0 for srcDelim and a fresh *rune for destDelim
// to preserve prior (no-translation) behavior in tests that don't care about it.
func runSyncFolder(
	t *testing.T,
	cfg *Config,
	src *SourceClient,
	state State,
	folder string,
	destAddr string,
	srcDelim rune,
	destDelim *rune,
) (newCount, skippedCount int, err error) {
	t.Helper()

	// Temporarily patch dialDestFn to use DialInsecure.
	orig := dialDestFn
	dialDestFn = func(host, user, pass string, skipTLS bool) (*DestClient, error) {
		c, err := imapclient.DialInsecure(host, nil)
		if err != nil {
			return nil, err
		}
		if err := c.Login(user, pass).Wait(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return newDestClient(c), nil
	}
	t.Cleanup(func() { dialDestFn = orig })

	cfg.DestHost = destAddr
	return syncFolder(cfg, src, state, folder, srcDelim, destDelim)
}

func TestSyncFolder_AllNew(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("new-1", "New 1", "body"), nil, time.Now())
	appendMsg(t, srcUser, "INBOX", testMessage("new-2", "New 2", "body"), nil, time.Now())

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)
	cfg := newSyncConfig(dstAddr)

	newCount, skipped, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 2, newCount)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 2, state.Count("INBOX"))
}

func TestSyncFolder_AllSkipped(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("skip-1", "Skip 1", "body"), nil, time.Now())
	appendMsg(t, srcUser, "INBOX", testMessage("skip-2", "Skip 2", "body"), nil, time.Now())

	// Pre-populate state with both keys.
	state := make(State)
	state.Add("INBOX", "<skip-1@test.example>")
	state.Add("INBOX", "<skip-2@test.example>")

	src := newSourceClient(dialInsecure(t, srcAddr))
	cfg := newSyncConfig(dstAddr)

	newCount, skipped, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 0, newCount)
	assert.Equal(t, 2, skipped)
}

func TestSyncFolder_PartialSkip(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("part-1", "Part 1", "body"), nil, time.Now())
	appendMsg(t, srcUser, "INBOX", testMessage("part-2", "Part 2", "body"), nil, time.Now())
	appendMsg(t, srcUser, "INBOX", testMessage("part-3", "Part 3", "body"), nil, time.Now())

	// Mark only the first as already synced.
	state := make(State)
	state.Add("INBOX", "<part-1@test.example>")

	src := newSourceClient(dialInsecure(t, srcAddr))
	cfg := newSyncConfig(dstAddr)

	newCount, skipped, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 2, newCount)
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 3, state.Count("INBOX")) // all three now recorded
}

func TestSyncFolder_DryRun(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, dstUser := newTestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("dry-1", "Dry 1", "body"), nil, time.Now())

	state := make(State)
	src := newSourceClient(dialInsecure(t, srcAddr))
	cfg := newSyncConfig(dstAddr)
	cfg.DryRun = true

	newCount, skipped, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 1, newCount, "dry-run reports 1 new")
	assert.Equal(t, 0, skipped)

	// State must NOT have been updated.
	assert.Equal(t, 0, state.Count("INBOX"), "dry-run must not update state")

	// Dest mailbox must still be empty.
	dstSrc := newSourceClient(dialInsecure(t, dstAddr))
	_ = dstUser // keep reference so server stays alive
	metas, err := dstSrc.FetchHeaders("INBOX")
	require.NoError(t, err)
	assert.Empty(t, metas, "dry-run must not deliver any messages")
}

func TestTestDestConnection_Success(t *testing.T) {
	dstAddr, _ := newTestServer(t)
	cfg := newSyncConfig(dstAddr)

	orig := dialDestFn
	dialDestFn = func(host, user, pass string, skipTLS bool) (*DestClient, error) {
		c, err := imapclient.DialInsecure(host, nil)
		if err != nil {
			return nil, err
		}
		if err := c.Login(user, pass).Wait(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return newDestClient(c), nil
	}
	t.Cleanup(func() { dialDestFn = orig })

	require.NoError(t, testDestConnection(cfg))
}

func TestTestDestConnection_Failure(t *testing.T) {
	cfg := newSyncConfig("127.0.0.1:1") // nothing listening

	orig := dialDestFn
	dialDestFn = func(host, user, pass string, skipTLS bool) (*DestClient, error) {
		c, err := imapclient.DialInsecure(host, nil)
		if err != nil {
			return nil, err
		}
		return newDestClient(c), nil
	}
	t.Cleanup(func() { dialDestFn = orig })

	require.Error(t, testDestConnection(cfg))
}

func TestSyncFolder_EmptyMailbox(t *testing.T) {
	srcAddr, _ := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)
	cfg := newSyncConfig(dstAddr)

	newCount, skipped, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 0, newCount)
	assert.Equal(t, 0, skipped)
}

func TestSyncFolder_StatePersistedAfterDelivery(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("persist-1", "Persist", "body"), nil, time.Now())

	state := make(State)
	stateFile := filepath.Join(t.TempDir(), "state.json")
	src := newSourceClient(dialInsecure(t, srcAddr))
	cfg := newSyncConfig(dstAddr)
	cfg.StateFile = stateFile

	newCount, _, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 1, newCount)

	// Save state manually (as main() does after each folder).
	require.NoError(t, state.Save(stateFile))

	// Reload and confirm the key is present.
	loaded, err := LoadState(stateFile)
	require.NoError(t, err)
	assert.True(t, loaded.Has("INBOX", "<persist-1@test.example>"))
}

func TestSyncFolder_NonInboxFolder(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	require.NoError(t, srcUser.Create("Sent", nil))
	appendMsg(t, srcUser, "Sent", testMessage("sent-1", "Sent Mail", "body"), nil, time.Now())

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)
	cfg := newSyncConfig(dstAddr)

	newCount, _, err := runSyncFolder(t, cfg, src, state, "Sent", dstAddr, 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 1, newCount)
	assert.True(t, state.Has("Sent", "<sent-1@test.example>"))
}

func TestSyncFolder_DelimiterTranslation(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	// Source uses '.' as its hierarchy delimiter (e.g. Rackspace/SmarterMail
	// style), while the dest test server (imapmemserver) always uses '/'.
	const srcFolder = "INBOX.Sent"
	require.NoError(t, srcUser.Create(srcFolder, nil))
	appendMsg(t, srcUser, srcFolder, testMessage("delim-1", "Delim Test", "body"), nil, time.Now())

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)
	cfg := newSyncConfig(dstAddr)

	var destDelim rune
	newCount, _, err := runSyncFolder(t, cfg, src, state, srcFolder, dstAddr, '.', &destDelim)
	require.NoError(t, err)
	assert.Equal(t, 1, newCount)

	// The destination delimiter must have been discovered and cached.
	assert.Equal(t, '/', destDelim)

	// State continues to be keyed by the untranslated source name.
	assert.True(t, state.Has(srcFolder, "<delim-1@test.example>"))

	// The message must have landed in the translated mailbox ("INBOX/Sent"),
	// not the literal source name.
	dstSrc := newSourceClient(dialInsecure(t, dstAddr))
	metas, err := dstSrc.FetchHeaders("INBOX/Sent")
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, "<delim-1@test.example>", metas[0].DedupKey)

	// A second folder synced in the same "run" must reuse the cached
	// destDelim rather than issuing another LIST query — verified indirectly
	// by confirming translation still applies correctly.
	const srcFolder2 = "INBOX.Sent.Archive"
	require.NoError(t, srcUser.Create(srcFolder2, nil))
	appendMsg(t, srcUser, srcFolder2, testMessage("delim-2", "Delim Test 2", "body"), nil, time.Now())

	newCount2, _, err := runSyncFolder(t, cfg, src, state, srcFolder2, dstAddr, '.', &destDelim)
	require.NoError(t, err)
	assert.Equal(t, 1, newCount2)

	metas2, err := dstSrc.FetchHeaders("INBOX/Sent/Archive")
	require.NoError(t, err)
	require.Len(t, metas2, 1)
	assert.Equal(t, "<delim-2@test.example>", metas2[0].DedupKey)
}

func TestSyncFolder_FlagsPreserved(t *testing.T) {
	srcAddr, srcUser := newTestServer(t)
	dstAddr, _ := newTestServer(t)

	flags := []imap.Flag{imap.FlagSeen, imap.FlagFlagged}
	raw := testMessage("flags-1", "Flags Test", "body")
	appendMsg(t, srcUser, "INBOX", raw, flags, time.Now())

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)
	cfg := newSyncConfig(dstAddr)

	_, _, err := runSyncFolder(t, cfg, src, state, "INBOX", dstAddr, 0, new(rune))
	require.NoError(t, err)

	// Fetch the delivered message from dest and check flags.
	dstClient := newSourceClient(dialInsecure(t, dstAddr))
	metas, err := dstClient.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metas, 1)

	msg, err := dstClient.FetchFull(metas[0].UID)
	require.NoError(t, err)

	flagStrs := make([]string, len(msg.Flags))
	for i, f := range msg.Flags {
		flagStrs[i] = string(f)
	}
	assert.Contains(t, strings.Join(flagStrs, " "), string(imap.FlagSeen))
	assert.Contains(t, strings.Join(flagStrs, " "), string(imap.FlagFlagged))
}
