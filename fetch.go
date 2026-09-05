package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// SourceClient wraps an IMAP client connected to the source server.
type SourceClient struct {
	c     *imapclient.Client
	delim rune
}

// newSourceClient wraps an existing IMAP client as a SourceClient.
// Used in tests to inject an already-dialled connection.
func newSourceClient(c *imapclient.Client) *SourceClient {
	return &SourceClient{c: c}
}

// DialSource connects to the source IMAP server over TLS and authenticates.
// If skipTLSVerify is true, certificate verification is skipped (for servers
// with expired or self-signed certificates).
func DialSource(host, user, pass string, skipTLSVerify bool) (*SourceClient, error) {
	opts := &imapclient.Options{}
	if skipTLSVerify {
		opts.TLSConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- opt-in via source.skip_tls for servers with untrusted certs
	}
	c, err := imapclient.DialTLS(host, opts)
	if err != nil {
		return nil, fmt.Errorf("dial source %s: %w", host, err)
	}
	if err := c.Login(user, pass).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login to source: %w", err)
	}
	return newSourceClient(c), nil
}

// Close closes the underlying IMAP connection.
func (s *SourceClient) Close() error {
	return s.c.Close()
}

// ListFolders returns all mailbox names on the source server. As a side
// effect it records the server's hierarchy delimiter (available via
// Delimiter) from the LIST response, avoiding a separate round-trip.
func (s *SourceClient) ListFolders() ([]string, error) {
	items, err := s.c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("LIST: %w", err)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Mailbox)
		if s.delim == 0 {
			s.delim = item.Delim
		}
	}
	return names, nil
}

// Delimiter returns the source server's mailbox hierarchy delimiter, as
// observed from the most recent ListFolders call. It is 0 if ListFolders has
// not been called yet or the server reported no delimiter.
func (s *SourceClient) Delimiter() rune {
	return s.delim
}

// MessageMeta holds the minimal info needed for deduplication.
type MessageMeta struct {
	UID      imap.UID
	DedupKey string
}

// FetchHeaders returns MessageMeta for every message in mailbox.
// Only message headers are transferred (no body).
func (s *SourceClient) FetchHeaders(mailbox string) ([]MessageMeta, error) {
	selectData, err := s.c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("SELECT %q: %w", mailbox, err)
	}
	if selectData.NumMessages == 0 {
		return nil, nil
	}

	// Fetch by sequence-number range: 1:* covers all messages.
	seqSet := imap.SeqSetNum()
	seqSet.AddRange(1, selectData.NumMessages)

	fetchOpts := &imap.FetchOptions{
		UID: true, // also return the UID so we can fetch the full message later
		BodySection: []*imap.FetchItemBodySection{
			{
				Specifier: imap.PartSpecifierHeader,
				Peek:      true, // don't set \Seen
			},
		},
	}

	fetchCmd := s.c.Fetch(seqSet, fetchOpts)
	var metas []MessageMeta
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			log.Printf("warning: collecting header in %q: %v", mailbox, err)
			continue
		}
		var headerBytes []byte
		for _, sec := range buf.BodySection {
			headerBytes = sec.Bytes
			break
		}
		metas = append(metas, MessageMeta{
			UID:      buf.UID,
			DedupKey: DedupKey(headerBytes),
		})
	}
	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("FETCH headers close %q: %w", mailbox, err)
	}
	return metas, nil
}

// FullMessage holds the complete data needed to deliver a message.
type FullMessage struct {
	RFC822       []byte
	Flags        []imap.Flag
	InternalDate time.Time
}

// FetchFull fetches the complete RFC822 body, flags, and internal date for a
// single message by UID. The mailbox must already be selected via FetchHeaders.
func (s *SourceClient) FetchFull(uid imap.UID) (*FullMessage, error) {
	// Passing a UIDSet to Fetch causes the library to issue UID FETCH.
	uidSet := imap.UIDSetNum(uid)
	fetchOpts := &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true}, // whole body, don't set \Seen
		},
	}
	fetchCmd := s.c.Fetch(uidSet, fetchOpts)
	msg := fetchCmd.Next()
	if msg == nil {
		_ = fetchCmd.Close()
		return nil, fmt.Errorf("UID %d not found in current mailbox", uid)
	}
	buf, err := msg.Collect()
	if err != nil {
		_ = fetchCmd.Close()
		return nil, fmt.Errorf("collect UID %d: %w", uid, err)
	}
	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("FETCH body close UID %d: %w", uid, err)
	}

	var body []byte
	for _, sec := range buf.BodySection {
		body = sec.Bytes
		break
	}
	return &FullMessage{
		RFC822:       body,
		Flags:        buf.Flags,
		InternalDate: buf.InternalDate,
	}, nil
}
