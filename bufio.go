package main

import (
	"bufio"
	"bytes"
)

// newBufioReader wraps a byte slice in a bufio.Reader, used by
// textproto.NewReader in state.go.
func newBufioReader(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}
