package main

import (
	"errors"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
)

func TestSanitizeFlags(t *testing.T) {
	tests := []struct {
		name string
		in   []imap.Flag
		want []imap.Flag
	}{
		{"nil", nil, nil},
		{"empty", []imap.Flag{}, nil},
		{
			"drops recent",
			[]imap.Flag{imap.FlagSeen, flagRecent},
			[]imap.Flag{imap.FlagSeen},
		},
		{
			"drops wildcard",
			[]imap.Flag{imap.FlagWildcard, imap.FlagAnswered},
			[]imap.Flag{imap.FlagAnswered},
		},
		{
			"drops recent case-insensitively",
			[]imap.Flag{imap.Flag("\\rEcEnT"), imap.FlagFlagged},
			[]imap.Flag{imap.FlagFlagged},
		},
		{
			"only recent yields nil",
			[]imap.Flag{flagRecent},
			nil,
		},
		{
			"keeps keywords and settable system flags",
			[]imap.Flag{imap.FlagSeen, imap.FlagDeleted, imap.Flag("$Label1")},
			[]imap.Flag{imap.FlagSeen, imap.FlagDeleted, imap.Flag("$Label1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeFlags(tt.in))
		})
	}
}

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"already exists lowercase", errors.New("already exists"), true},
		{"already exists uppercase", errors.New("ALREADY EXISTS"), true},
		{"already exists mixed case", errors.New("Mailbox Already Exists"), true},
		{"mailbox exists", errors.New("mailbox exists"), true},
		{"alreadyexists no space", errors.New("alreadyexists"), true},
		{"alreadyexists bracketed", errors.New("[ALREADYEXISTS] mailbox already exists"), true},
		{"unrelated error", errors.New("something went wrong"), false},
		{"permission denied", errors.New("permission denied"), false},
		{"no such mailbox", errors.New("no such mailbox"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAlreadyExists(tc.err))
		})
	}
}
