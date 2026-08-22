package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/textproto"
	"os"
	"strings"
)

// State maps mailbox name → set of dedup keys already delivered.
type State map[string]map[string]struct{}

// Has reports whether key has been delivered to mailbox.
func (s State) Has(mailbox, key string) bool {
	if s[mailbox] == nil {
		return false
	}
	_, ok := s[mailbox][key]
	return ok
}

// Add records key as delivered to mailbox.
func (s State) Add(mailbox, key string) {
	if s[mailbox] == nil {
		s[mailbox] = make(map[string]struct{})
	}
	s[mailbox][key] = struct{}{}
}

// Count returns the number of known keys for mailbox.
func (s State) Count(mailbox string) int {
	return len(s[mailbox])
}

// LoadState reads the state file from path. Returns an empty State if the
// file does not exist.
func LoadState(path string) (State, error) {
	s := make(State)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the CLI-provided state file location, not user input
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}

	// On-disk format: map[mailbox][]string for compactness.
	raw := make(map[string][]string)
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	for mb, keys := range raw {
		s[mb] = make(map[string]struct{}, len(keys))
		for _, k := range keys {
			s[mb][k] = struct{}{}
		}
	}
	return s, nil
}

// Save writes the state file atomically (write to .tmp, rename).
func (s State) Save(path string) error {
	raw := make(map[string][]string, len(s))
	for mb, keys := range s {
		list := make([]string, 0, len(keys))
		for k := range keys {
			list = append(list, k)
		}
		raw[mb] = list
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}

// DedupKey returns a stable identifier for a message given its raw header
// bytes. Uses the Message-ID header when present; otherwise falls back to
// a hex-encoded SHA-256 prefix of "Date\nFrom\nSubject".
func DedupKey(headerBytes []byte) string {
	tp := textproto.NewReader(newBufioReader(headerBytes))
	hdr, err := tp.ReadMIMEHeader()
	if err == nil {
		if ids, ok := hdr["Message-Id"]; ok && len(ids) > 0 {
			mid := strings.TrimSpace(ids[0])
			if mid != "" {
				return mid
			}
		}
	}

	// Fallback: hash Date + From + Subject.
	date := strings.TrimSpace(strings.Join(hdr["Date"], ""))
	from := strings.TrimSpace(strings.Join(hdr["From"], ""))
	subj := strings.TrimSpace(strings.Join(hdr["Subject"], ""))
	h := sha256.Sum256([]byte(date + "\n" + from + "\n" + subj))
	return fmt.Sprintf("sha256:%x", h[:8])
}
