package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── State.Has / Add / Count ──────────────────────────────────────────────────

func TestState_HasAddCount(t *testing.T) {
	s := make(State)

	assert.False(t, s.Has("INBOX", "key1"), "Has should be false before Add")
	assert.Equal(t, 0, s.Count("INBOX"))

	s.Add("INBOX", "key1")

	assert.True(t, s.Has("INBOX", "key1"), "Has should be true after Add")
	assert.Equal(t, 1, s.Count("INBOX"))
}

func TestState_AddMultipleMailboxes(t *testing.T) {
	s := make(State)
	s.Add("INBOX", "key1")
	s.Add("Sent", "key2")

	assert.True(t, s.Has("INBOX", "key1"))
	assert.False(t, s.Has("INBOX", "key2"), "key2 belongs to Sent, not INBOX")
	assert.True(t, s.Has("Sent", "key2"))
	assert.False(t, s.Has("Sent", "key1"))
	assert.Equal(t, 1, s.Count("INBOX"))
	assert.Equal(t, 1, s.Count("Sent"))
}

func TestState_AddDuplicate(t *testing.T) {
	s := make(State)
	s.Add("INBOX", "key1")
	s.Add("INBOX", "key1") // second add is a no-op

	assert.Equal(t, 1, s.Count("INBOX"))
}

func TestState_CountUnknownMailbox(t *testing.T) {
	s := make(State)
	assert.Equal(t, 0, s.Count("NoSuchFolder"))
}

// ── LoadState / Save ─────────────────────────────────────────────────────────

func TestLoadState_NotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	s, err := LoadState(path)
	require.NoError(t, err)
	assert.Empty(t, s)
}

func TestLoadState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	original := make(State)
	original.Add("INBOX", "<msg1@example.com>")
	original.Add("INBOX", "<msg2@example.com>")
	original.Add("Sent", "<msg3@example.com>")

	require.NoError(t, original.Save(path))

	loaded, err := LoadState(path)
	require.NoError(t, err)

	assert.Equal(t, 2, loaded.Count("INBOX"))
	assert.Equal(t, 1, loaded.Count("Sent"))
	assert.True(t, loaded.Has("INBOX", "<msg1@example.com>"))
	assert.True(t, loaded.Has("INBOX", "<msg2@example.com>"))
	assert.True(t, loaded.Has("Sent", "<msg3@example.com>"))
}

func TestLoadState_Corrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte("not json {{{"), 0o600))

	_, err := LoadState(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse state file")
}

func TestSave_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := make(State)
	s.Add("INBOX", "key1")
	require.NoError(t, s.Save(path))

	// Verify the .tmp file was removed after successful save.
	_, err := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), ".tmp file should not exist after save")

	// Verify the final file contains valid JSON.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string][]string
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw["INBOX"], "key1")
}

func TestSave_CreatesMissingParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "sub", "state.json")

	s := make(State)
	s.Add("INBOX", "key1")
	require.NoError(t, s.Save(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string][]string
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw["INBOX"], "key1")
}

func TestSave_OverwriteExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	s1 := make(State)
	s1.Add("INBOX", "old-key")
	require.NoError(t, s1.Save(path))

	s2 := make(State)
	s2.Add("INBOX", "new-key")
	require.NoError(t, s2.Save(path))

	loaded, err := LoadState(path)
	require.NoError(t, err)
	assert.True(t, loaded.Has("INBOX", "new-key"))
	assert.False(t, loaded.Has("INBOX", "old-key"))
}

// ── DedupKey ─────────────────────────────────────────────────────────────────

func TestDedupKey_MessageID(t *testing.T) {
	header := []byte("Message-ID: <unique-id-123@example.com>\r\nFrom: a@b.com\r\nSubject: Hi\r\n\r\n")
	key := DedupKey(header)
	assert.Equal(t, "<unique-id-123@example.com>", key)
}

func TestDedupKey_MessageIDWhitespace(t *testing.T) {
	// Some servers emit Message-ID with surrounding whitespace.
	header := []byte("Message-ID:  <padded@example.com>  \r\nFrom: a@b.com\r\n\r\n")
	key := DedupKey(header)
	assert.Equal(t, "<padded@example.com>", key)
}

func TestDedupKey_Fallback(t *testing.T) {
	// No Message-ID — should fall back to sha256 of Date+From+Subject.
	header := []byte("Date: Mon, 1 Jan 2024 00:00:00 +0000\r\nFrom: sender@example.com\r\nSubject: Hello\r\n\r\n")
	key := DedupKey(header)
	assert.True(t, len(key) > 0)
	assert.Contains(t, key, "sha256:")
}

func TestDedupKey_EmptyMessageID(t *testing.T) {
	// Empty Message-ID value should fall through to the hash fallback.
	header := []byte("Message-ID: \r\nDate: Mon, 1 Jan 2024 00:00:00 +0000\r\nFrom: a@b.com\r\nSubject: Test\r\n\r\n")
	key := DedupKey(header)
	assert.Contains(t, key, "sha256:")
}

func TestDedupKey_Stable(t *testing.T) {
	header := []byte("Date: Mon, 1 Jan 2024 00:00:00 +0000\r\nFrom: sender@example.com\r\nSubject: Hello\r\n\r\n")
	key1 := DedupKey(header)
	key2 := DedupKey(header)
	assert.Equal(t, key1, key2, "DedupKey must be deterministic")
}

func TestDedupKey_DifferentInputsDifferentKeys(t *testing.T) {
	h1 := []byte("Date: Mon, 1 Jan 2024 00:00:00 +0000\r\nFrom: a@example.com\r\nSubject: One\r\n\r\n")
	h2 := []byte("Date: Tue, 2 Jan 2024 00:00:00 +0000\r\nFrom: b@example.com\r\nSubject: Two\r\n\r\n")
	assert.NotEqual(t, DedupKey(h1), DedupKey(h2))
}
