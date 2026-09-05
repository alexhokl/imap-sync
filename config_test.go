package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadConfigFile_Valid(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
  skip_tls: true

accounts:
  - name: alice
    source:
      host: imap.example.com:993
      user: alice@example.com
      pass: alicepass
    dest:
      user: alice-backup
      pass: alicebackuppass
  - name: bob
    source:
      host: imap.example.com:993
      user: bob@example.com
      pass: bobpass
    dest:
      user: bob-backup
      pass: bobbackuppass
    state_file: ./custom-bob-state.json
`)

	cfg, err := loadConfigFile(path)
	require.NoError(t, err)

	assert.Equal(t, "localhost:993", cfg.Dest.Host)
	assert.True(t, cfg.Dest.SkipTLS)
	require.Len(t, cfg.Accounts, 2)

	alice := cfg.Accounts[0]
	assert.Equal(t, "alice", alice.Name)
	assert.Equal(t, "imap.example.com:993", alice.SourceHost)
	assert.Equal(t, "alice@example.com", alice.SourceUser)
	assert.Equal(t, "alicepass", alice.SourcePass)
	assert.False(t, alice.SourceSkipTLS, "source.skip_tls defaults to false when absent")
	assert.Equal(t, "alice-backup", alice.DestUser)
	assert.Equal(t, "alicebackuppass", alice.DestPass)
	assert.Equal(t, "/state/sync-state-alice.json", alice.StateFile, "default state file derived from name")

	bob := cfg.Accounts[1]
	assert.Equal(t, "./custom-bob-state.json", bob.StateFile, "explicit state_file is preserved")
}

func TestLoadConfigFile_SourceSkipTLS(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993

accounts:
  - name: alice
    source:
      host: imap.example.com:993
      user: alice@example.com
      pass: alicepass
      skip_tls: true
    dest:
      user: alice-backup
      pass: alicebackuppass
  - name: bob
    source:
      host: imap.other.example:993
      user: bob@example.com
      pass: bobpass
      skip_tls: false
    dest:
      user: bob-backup
      pass: bobbackuppass
`)

	cfg, err := loadConfigFile(path)
	require.NoError(t, err)
	require.Len(t, cfg.Accounts, 2)
	assert.True(t, cfg.Accounts[0].SourceSkipTLS, "source.skip_tls: true is parsed")
	assert.False(t, cfg.Accounts[1].SourceSkipTLS, "explicit skip_tls: false is parsed")
}

func TestLoadConfigFile_MissingDestHost(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: ""
accounts:
  - name: alice
    source: { host: h, user: u, pass: p }
    dest: { user: du, pass: dp }
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dest.host")
}

func TestLoadConfigFile_NoAccounts(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
accounts: []
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one account")
}

func TestLoadConfigFile_MissingAccountName(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
accounts:
  - source: { host: h, user: u, pass: p }
    dest: { user: du, pass: dp }
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestLoadConfigFile_DuplicateAccountNames(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
accounts:
  - name: alice
    source: { host: h1, user: u1, pass: p1 }
    dest: { user: du1, pass: dp1 }
  - name: alice
    source: { host: h2, user: u2, pass: p2 }
    dest: { user: du2, pass: dp2 }
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate account name")
}

func TestLoadConfigFile_MissingSourceField(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
accounts:
  - name: alice
    source: { host: "", user: u, pass: p }
    dest: { user: du, pass: dp }
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `account "alice": source.host is required`)
}

func TestLoadConfigFile_MissingDestField(t *testing.T) {
	path := writeConfigFile(t, `
dest:
  host: localhost:993
accounts:
  - name: alice
    source: { host: h, user: u, pass: p }
    dest: { user: "", pass: dp }
`)
	_, err := loadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `account "alice": dest.user is required`)
}

func TestLoadConfigFile_NotFound(t *testing.T) {
	_, err := loadConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestLoadConfigFile_InvalidYAML(t *testing.T) {
	path := writeConfigFile(t, "not: [valid: yaml")
	_, err := loadConfigFile(path)
	require.Error(t, err)
}

func TestApplyDefaults_SanitizesStateFileName(t *testing.T) {
	cfg := &AppConfig{
		Accounts: []AccountConfig{
			{Name: "alice/bob weird!name"},
		},
	}
	cfg.applyDefaults()
	assert.Equal(t, "/state/sync-state-alice_bob_weird_name.json", cfg.Accounts[0].StateFile)
}
