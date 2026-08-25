package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAccount_CancelledBeforeFolderLoop verifies that a pre-cancelled
// context causes runAccount to skip the folder loop entirely: no messages
// are delivered, no state file is written, and no error is returned.
func TestRunAccount_CancelledBeforeFolderLoop(t *testing.T) {
	withInsecureDialers(t)

	srcAddr, srcUser := newTestServer(t)
	dstAddr, _, _ := newSharedDestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("cancel-1", "Cancel", "body"), nil, time.Now())

	tmpDir := t.TempDir()
	acc := &AccountConfig{
		Name:       "cancel",
		SourceHost: srcAddr,
		SourceUser: testUser,
		SourcePass: testPass,
		DestUser:   "alice-backup",
		DestPass:   "alicebackuppass",
		StateFile:  filepath.Join(tmpDir, "cancel.json"),
	}
	cfg := &Config{
		DestHost:  dstAddr,
		DestUser:  acc.DestUser,
		DestPass:  acc.DestPass,
		StateFile: acc.StateFile,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	newCount, skippedCount, err := runAccount(ctx, cfg, acc)
	require.NoError(t, err, "graceful shutdown must not surface as an error")
	assert.Equal(t, 0, newCount, "no messages should be delivered on shutdown")
	assert.Equal(t, 0, skippedCount, "folder loop must be skipped entirely")

	// State file must not have been written.
	_, err = LoadState(acc.StateFile)
	require.NoError(t, err, "state file should be absent/empty")

	// Destination must remain empty.
	dstSrc := newSourceClient(dialInsecureAs(t, dstAddr, "alice-backup", "alicebackuppass"))
	metas, err := dstSrc.FetchHeaders("INBOX")
	require.NoError(t, err)
	assert.Empty(t, metas, "nothing should have been delivered")
}

// TestSyncFolder_CancelledMidMessageLoop verifies that when the context is
// cancelled partway through a folder's message loop, progress made so far is
// persisted to the state file and the function returns cleanly. A fresh
// re-run with the saved state skips the already-delivered messages and
// delivers the rest.
func TestSyncFolder_CancelledMidMessageLoop(t *testing.T) {
	withInsecureDialers(t)

	srcAddr, srcUser := newTestServer(t)
	dstAddr, _, _ := newSharedDestServer(t)

	// Seed 5 messages on the source.
	const total = 5
	for i := 1; i <= total; i++ {
		appendMsg(t, srcUser, "INBOX",
			testMessage(
				"mid-"+string(rune('0'+i)),
				"Mid "+string(rune('0'+i)),
				"body",
			), nil, time.Now())
	}

	stateFile := filepath.Join(t.TempDir(), "mid.json")
	cfg := newSyncConfig(dstAddr)
	cfg.StateFile = stateFile
	cfg.DestUser = "alice-backup"
	cfg.DestPass = "alicebackuppass"
	cfg.DestHost = dstAddr

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context after 2 successful appends by counting inside a
	// patched dialDestFn whose returned DestClient carries an onAppend hook.
	var appendCount atomic.Int32
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
		d := newDestClient(c)
		d.onAppend = func() {
			if appendCount.Add(1) >= 2 {
				cancel()
			}
		}
		return d, nil
	}
	t.Cleanup(func() { dialDestFn = origDest })

	newCount, _, err := syncFolder(ctx, cfg, src, state, "INBOX", 0, new(rune))
	require.NoError(t, err, "shutdown must not surface as an error")
	assert.Equal(t, 2, newCount, "exactly 2 messages should be delivered before shutdown")

	// State file must contain exactly the 2 delivered keys.
	loaded, err := LoadState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.Count("INBOX"))

	// Re-run with a non-cancelled context and the persisted state: the 2
	// delivered messages should be skipped, the remaining 3 delivered.
	src2 := newSourceClient(dialInsecure(t, srcAddr))
	cfg2 := newSyncConfig(dstAddr)
	cfg2.StateFile = stateFile
	cfg2.DestUser = "alice-backup"
	cfg2.DestPass = "alicebackuppass"
	cfg2.DestHost = dstAddr

	newCount2, skipped2, err := syncFolder(context.Background(), cfg2, src2, loaded, "INBOX", 0, new(rune))
	require.NoError(t, err)
	assert.Equal(t, 3, newCount2, "remaining 3 messages should be delivered on re-run")
	assert.Equal(t, 2, skipped2, "2 previously-delivered messages should be skipped")
	assert.Equal(t, total, loaded.Count("INBOX"))
}

// TestSyncFolder_CancelledBeforeDestDial verifies that when the context is
// cancelled after headers are fetched but before the destination is dialled,
// no destination connection is made and state is left unchanged.
func TestSyncFolder_CancelledBeforeDestDial(t *testing.T) {
	withInsecureDialers(t)

	srcAddr, srcUser := newTestServer(t)
	dstAddr, _, _ := newSharedDestServer(t)

	appendMsg(t, srcUser, "INBOX", testMessage("predial-1", "PreDial 1", "body"), nil, time.Now())
	appendMsg(t, srcUser, "INBOX", testMessage("predial-2", "PreDial 2", "body"), nil, time.Now())

	stateFile := filepath.Join(t.TempDir(), "predial.json")
	cfg := newSyncConfig(dstAddr)
	cfg.StateFile = stateFile
	cfg.DestUser = "alice-backup"
	cfg.DestPass = "alicebackuppass"
	cfg.DestHost = dstAddr

	src := newSourceClient(dialInsecure(t, srcAddr))
	state := make(State)

	// Pre-cancel the context. FetchHeaders is not ctx-aware and still runs,
	// but the ctx.Err() check after the dry-run/empty short-circuit (and
	// before dialing dest) will trip, so no destination connection is made.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fail the test if dialDestFn is invoked at all.
	origDest := dialDestFn
	dialDestFn = func(host, user, pass string, skipTLS bool) (*DestClient, error) {
		t.Fatal("dialDestFn must not be called when context is cancelled before dest dial")
		return nil, nil
	}
	t.Cleanup(func() { dialDestFn = origDest })

	newCount, skippedCount, err := syncFolder(ctx, cfg, src, state, "INBOX", 0, new(rune))
	require.NoError(t, err, "shutdown must not surface as an error")
	// Headers were fetched, so the "new" count reflects messages that would
	// have been pushed; nothing was actually delivered though.
	assert.Equal(t, 2, newCount, "new count should reflect fetched headers pending delivery")
	assert.Equal(t, 0, skippedCount)

	// State must be unchanged (nothing delivered, nothing saved).
	assert.Equal(t, 0, state.Count("INBOX"))

	// No state file should have been written.
	loaded, err := LoadState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, 0, loaded.Count("INBOX"))
}