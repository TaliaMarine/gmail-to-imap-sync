// Package imapdest delivers messages into the destination IMAP server
// (Dovecot) and decides, per folder, whether a message is already there.
//
// Deduplication is destination-side: every delivered copy carries an
// X-Gmail-Archive-ID header, and the set of those headers per folder is the
// only record of what has been delivered (FULL_SPECS.md §6.2). Nothing about
// successful messages is stored in PostgreSQL.
package imapdest

import (
	"bytes"
	"errors"
	"net/textproto"
	"strings"
)

// ErrMalformed marks a message that cannot be delivered because its header
// section is broken.
var ErrMalformed = errors.New("malformed message: header starts with whitespace")

// HeaderName is the private header added to every delivered message.
const HeaderName = "X-Gmail-Archive-ID"

// HeaderValue builds the header value for a Gmail message of an account. The
// account prefix keeps two Gmail accounts delivered into one mailbox from
// colliding.
func HeaderValue(accountID, gmailMessageID string) string {
	return accountID + "." + gmailMessageID
}

// AddHeader returns raw with "X-Gmail-Archive-ID: value" prepended as the
// first header line. Everything else, byte for byte, is left alone. If the
// message already carries exactly that header it is returned unchanged, so
// the header is only ever added once.
//
// The header is written with CRLF, as IMAP requires.
//
// A message whose first line starts with whitespace is malformed (a header
// continuation with nothing to continue); prepending would fold that line
// into our header and corrupt the ID, so it is rejected with ErrMalformed.
func AddHeader(raw []byte, value string) ([]byte, error) {
	if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') {
		return nil, ErrMalformed
	}
	for _, v := range headerValues(headerBlock(raw)) {
		if v == value {
			return raw, nil
		}
	}
	line := HeaderName + ": " + value + "\r\n"
	out := make([]byte, 0, len(line)+len(raw))
	out = append(out, line...)
	return append(out, raw...), nil
}

// headerBlock returns the header section of raw (up to the first blank line).
func headerBlock(raw []byte) []byte {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		// A blank line: "\n\n" or "\n\r\n". The first line of the message
		// being blank also ends the (empty) header block.
		if i+1 < len(raw) && raw[i+1] == '\n' {
			return raw[:i+1]
		}
		if i+2 < len(raw) && raw[i+1] == '\r' && raw[i+2] == '\n' {
			return raw[:i+1]
		}
	}
	return raw
}

// headerValues returns every value of the archive header in a header block,
// unfolded and trimmed.
func headerValues(block []byte) []string {
	if len(block) == 0 {
		return nil
	}
	// textproto needs a terminating blank line.
	b := block
	if !bytes.HasSuffix(b, []byte("\n\n")) && !bytes.HasSuffix(b, []byte("\r\n\r\n")) {
		b = append(append([]byte{}, b...), '\r', '\n')
		if !bytes.HasSuffix(b, []byte("\r\n\r\n")) {
			b = append(b, '\r', '\n')
		}
	}
	r := textproto.NewReader(bytesReader(b))
	h, _ := r.ReadMIMEHeader() // best effort: a malformed block yields what was read
	vals := h.Values(HeaderName)
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, strings.TrimSpace(v))
	}
	return out
}
