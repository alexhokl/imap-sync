package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialInsecureAs dials addr without TLS and logs in as the given credentials.
func dialInsecureAs(t *testing.T, addr, user, pass string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	require.NoError(t, err, "DialInsecure")
	require.NoError(t, c.Login(user, pass).Wait(), "Login")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// newSharedDestServer spins up one in-process imapmemserver hosting two
// distinct users, simulating one destination server shared by two accounts
// that each authenticate with their own paired dest login.
func newSharedDestServer(t *testing.T) (addr string, userA, userB *imapmemserver.User) {
	t.Helper()

	memSrv := imapmemserver.New()

	userA = imapmemserver.NewUser("alice-backup", "alicebackuppass")
	require.NoError(t, userA.Create("INBOX", nil))
	memSrv.AddUser(userA)

	userB = imapmemserver.NewUser("bob-backup", "bobbackuppass")
	require.NoError(t, userB.Create("INBOX", nil))
	memSrv.AddUser(userB)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memSrv.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() {
		_ = ln.Close()
		_ = srv.Close()
	})

	return ln.Addr().String(), userA, userB
}

func withInsecureDialers(t *testing.T) {
	t.Helper()

	origDest := dialDestFn
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
	t.Cleanup(func() { dialDestFn = origDest })

	origSrc := dialSourceFn
	dialSourceFn = func(host, user, pass string) (*SourceClient, error) {
		c, err := imapclient.DialInsecure(host, nil)
		if err != nil {
			return nil, err
		}
		if err := c.Login(user, pass).Wait(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return newSourceClient(c), nil
	}
	t.Cleanup(func() { dialSourceFn = origSrc })
}

func TestRunAccount_MultipleAccountsSharedDest_NoCollision(t *testing.T) {
	withInsecureDialers(t)

	// Two independent source servers/accounts.
	aliceSrcAddr, aliceSrcUser := newTestServer(t)
	bobSrcAddr, bobSrcUser := newTestServer(t)

	appendMsg(t, aliceSrcUser, "INBOX", testMessage("alice-1", "Alice Mail", "body"), nil, time.Now())
	appendMsg(t, bobSrcUser, "INBOX", testMessage("bob-1", "Bob Mail", "body"), nil, time.Now())

	// One shared destination server with two distinct dest logins.
	destAddr, destUserA, destUserB := newSharedDestServer(t)

	tmpDir := t.TempDir()

	aliceAcc := &AccountConfig{
		Name:       "alice",
		SourceHost: aliceSrcAddr,
		SourceUser: testUser,
		SourcePass: testPass,
		DestUser:   "alice-backup",
		DestPass:   "alicebackuppass",
		StateFile:  filepath.Join(tmpDir, "alice.json"),
	}
	bobAcc := &AccountConfig{
		Name:       "bob",
		SourceHost: bobSrcAddr,
		SourceUser: testUser,
		SourcePass: testPass,
		DestUser:   "bob-backup",
		DestPass:   "bobbackuppass",
		StateFile:  filepath.Join(tmpDir, "bob.json"),
	}

	destCfgFor := func(acc *AccountConfig) *Config {
		return &Config{
			DestHost:  destAddr,
			DestUser:  acc.DestUser,
			DestPass:  acc.DestPass,
			StateFile: acc.StateFile,
		}
	}

	newA, skippedA, err := runAccount(context.Background(), destCfgFor(aliceAcc), aliceAcc)
	require.NoError(t, err)
	assert.Equal(t, 1, newA)
	assert.Equal(t, 0, skippedA)

	newB, skippedB, err := runAccount(context.Background(), destCfgFor(bobAcc), bobAcc)
	require.NoError(t, err)
	assert.Equal(t, 1, newB)
	assert.Equal(t, 0, skippedB)

	// Each account's mail landed only in its own dest mailbox.
	_ = destUserA
	_ = destUserB
	aliceDest := newSourceClient(dialInsecureAs(t, destAddr, "alice-backup", "alicebackuppass"))
	metasA, err := aliceDest.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metasA, 1)
	assert.Equal(t, "<alice-1@test.example>", metasA[0].DedupKey)

	bobDest := newSourceClient(dialInsecureAs(t, destAddr, "bob-backup", "bobbackuppass"))
	metasB, err := bobDest.FetchHeaders("INBOX")
	require.NoError(t, err)
	require.Len(t, metasB, 1)
	assert.Equal(t, "<bob-1@test.example>", metasB[0].DedupKey)

	// Each account's state file only holds its own dedup key.
	stateA, err := LoadState(aliceAcc.StateFile)
	require.NoError(t, err)
	assert.True(t, stateA.Has("INBOX", "<alice-1@test.example>"))
	assert.False(t, stateA.Has("INBOX", "<bob-1@test.example>"))

	stateB, err := LoadState(bobAcc.StateFile)
	require.NoError(t, err)
	assert.True(t, stateB.Has("INBOX", "<bob-1@test.example>"))
	assert.False(t, stateB.Has("INBOX", "<alice-1@test.example>"))
}

func TestRunAccount_OneAccountFailureDoesNotAffectOther(t *testing.T) {
	withInsecureDialers(t)

	bobSrcAddr, bobSrcUser := newTestServer(t)
	appendMsg(t, bobSrcUser, "INBOX", testMessage("bob-2", "Bob Mail 2", "body"), nil, time.Now())

	destAddr, _, _ := newSharedDestServer(t)
	tmpDir := t.TempDir()

	// Alice points at a source host nothing is listening on -> dial error.
	aliceAcc := &AccountConfig{
		Name:       "alice",
		SourceHost: "127.0.0.1:1",
		SourceUser: "nobody",
		SourcePass: "nopass",
		DestUser:   "alice-backup",
		DestPass:   "alicebackuppass",
		StateFile:  filepath.Join(tmpDir, "alice.json"),
	}
	bobAcc := &AccountConfig{
		Name:       "bob",
		SourceHost: bobSrcAddr,
		SourceUser: testUser,
		SourcePass: testPass,
		DestUser:   "bob-backup",
		DestPass:   "bobbackuppass",
		StateFile:  filepath.Join(tmpDir, "bob.json"),
	}

	_, _, err := runAccount(context.Background(), &Config{
		DestHost: destAddr, DestUser: aliceAcc.DestUser, DestPass: aliceAcc.DestPass, StateFile: aliceAcc.StateFile,
	}, aliceAcc)
	assert.Error(t, err, "alice's source dial should fail")

	newB, skippedB, err := runAccount(context.Background(), &Config{
		DestHost: destAddr, DestUser: bobAcc.DestUser, DestPass: bobAcc.DestPass, StateFile: bobAcc.StateFile,
	}, bobAcc)
	require.NoError(t, err, "bob's run must succeed independently of alice's failure")
	assert.Equal(t, 1, newB)
	assert.Equal(t, 0, skippedB)
}

// ── preflightAccounts ────────────────────────────────────────────────────────

// TestPreflightAccounts_Success verifies that preflight passes when every
// account's source and destination logins work.
func TestPreflightAccounts_Success(t *testing.T) {
	withInsecureDialers(t)

	aliceSrcAddr, _ := newTestServer(t)
	bobSrcAddr, _ := newTestServer(t)
	destAddr, _, _ := newSharedDestServer(t)

	appCfg := &AppConfig{
		Dest: DestHostConfig{Host: destAddr},
		Accounts: []AccountConfig{
			{
				Name:       "alice",
				SourceHost: aliceSrcAddr,
				SourceUser: testUser,
				SourcePass: testPass,
				DestUser:   "alice-backup",
				DestPass:   "alicebackuppass",
			},
			{
				Name:       "bob",
				SourceHost: bobSrcAddr,
				SourceUser: testUser,
				SourcePass: testPass,
				DestUser:   "bob-backup",
				DestPass:   "bobbackuppass",
			},
		},
	}

	require.NoError(t, preflightAccounts(context.Background(), appCfg))
}

// TestPreflightAccounts_BadDestCred verifies that a wrong destination
// password in one account fails the preflight and prevents any data pull —
// the other account's mailbox must remain empty.
func TestPreflightAccounts_BadDestCred(t *testing.T) {
	withInsecureDialers(t)

	aliceSrcAddr, aliceSrcUser := newTestServer(t)
	appendMsg(t, aliceSrcUser, "INBOX", testMessage("alice-pf", "Alice PF", "body"), nil, time.Now())

	destAddr, _, _ := newSharedDestServer(t)

	appCfg := &AppConfig{
		Dest: DestHostConfig{Host: destAddr},
		Accounts: []AccountConfig{
			{
				Name:       "alice",
				SourceHost: aliceSrcAddr,
				SourceUser: testUser,
				SourcePass: testPass,
				DestUser:   "alice-backup",
				DestPass:   "wrong-dest-pass",
			},
			{
				Name:       "bob",
				SourceHost: "unused",
				SourceUser: testUser,
				SourcePass: testPass,
				DestUser:   "bob-backup",
				DestPass:   "bobbackuppass",
			},
		},
	}

	err := preflightAccounts(context.Background(), appCfg)
	require.Error(t, err, "bad dest credential must fail the preflight")
	assert.Contains(t, err.Error(), "alice", "error must identify the failing account")

	// Nothing may have been pulled or delivered.
	aliceDest := newSourceClient(dialInsecureAs(t, destAddr, "alice-backup", "alicebackuppass"))
	metas, err := aliceDest.FetchHeaders("INBOX")
	require.NoError(t, err)
	assert.Empty(t, metas, "no messages may be delivered when preflight fails")
}

// TestPreflightAccounts_BadSourceCred verifies that a wrong source password
// fails the preflight.
func TestPreflightAccounts_BadSourceCred(t *testing.T) {
	withInsecureDialers(t)

	aliceSrcAddr, _ := newTestServer(t)
	destAddr, _, _ := newSharedDestServer(t)

	appCfg := &AppConfig{
		Dest: DestHostConfig{Host: destAddr},
		Accounts: []AccountConfig{
			{
				Name:       "alice",
				SourceHost: aliceSrcAddr,
				SourceUser: testUser,
				SourcePass: "wrong-source-pass",
				DestUser:   "alice-backup",
				DestPass:   "alicebackuppass",
			},
		},
	}

	err := preflightAccounts(context.Background(), appCfg)
	require.Error(t, err, "bad source credential must fail the preflight")
	assert.Contains(t, err.Error(), "alice")
}

// TestPreflightAccounts_CancelledCtx verifies that a pre-cancelled context
// aborts the preflight before any connection is attempted.
func TestPreflightAccounts_CancelledCtx(t *testing.T) {
	withInsecureDialers(t)

	appCfg := &AppConfig{
		Dest: DestHostConfig{Host: "127.0.0.1:1"},
		Accounts: []AccountConfig{
			{
				Name:       "alice",
				SourceHost: "127.0.0.1:1",
				SourceUser: testUser,
				SourcePass: testPass,
				DestUser:   "alice-backup",
				DestPass:   "alicebackuppass",
			},
		},
	}

	// Fail the test if either dialer is invoked at all.
	origDest := dialDestFn
	dialDestFn = func(host, user, pass string, skipTLS bool) (*DestClient, error) {
		t.Fatal("dialDestFn must not be called when context is cancelled")
		return nil, nil
	}
	t.Cleanup(func() { dialDestFn = origDest })

	origSrc := dialSourceFn
	dialSourceFn = func(host, user, pass string) (*SourceClient, error) {
		t.Fatal("dialSourceFn must not be called when context is cancelled")
		return nil, nil
	}
	t.Cleanup(func() { dialSourceFn = origSrc })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, preflightAccounts(ctx, appCfg), context.Canceled)
}
