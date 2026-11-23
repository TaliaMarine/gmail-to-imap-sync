package failures

import (
	"bufio"
	"bytes"
	"mime"
	"net/mail"
	"net/textproto"
	"time"
)

// Meta is the descriptive data kept with a failure: enough to recognise the
// message, never its body.
type Meta struct {
	Subject    string
	Sender     string
	Recipients []string
	Date       time.Time // zero if absent
}

const (
	maxRecipients = 20
	maxHeaderRead = 64 << 10 // headers beyond this are not looked at
)

// ExtractMeta reads the subject, sender, recipients and date from the header
// section of raw. It is tolerant: whatever cannot be parsed stays empty.
func ExtractMeta(raw []byte) Meta {
	head := raw
	if len(head) > maxHeaderRead {
		head = head[:maxHeaderRead]
	}
	// Only the header section: cut at the first blank line.
	if i := indexBlankLine(head); i >= 0 {
		head = head[:i]
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(append([]byte{}, head...), '\r', '\n', '\r', '\n'))))
	h, _ := tp.ReadMIMEHeader()
	hdr := mail.Header(h)

	dec := new(mime.WordDecoder)
	var m Meta
	if s := hdr.Get("Subject"); s != "" {
		if d, err := dec.DecodeHeader(s); err == nil {
			s = d
		}
		m.Subject = Truncate(s)
	}
	if from, err := hdr.AddressList("From"); err == nil && len(from) > 0 {
		m.Sender = from[0].Address
		if from[0].Name != "" {
			m.Sender = from[0].String()
		}
	} else if f := hdr.Get("From"); f != "" {
		m.Sender = Truncate(f)
	}
	for _, field := range []string{"To", "Cc"} {
		list, err := hdr.AddressList(field)
		if err != nil {
			continue
		}
		for _, a := range list {
			if len(m.Recipients) >= maxRecipients {
				break
			}
			m.Recipients = append(m.Recipients, a.Address)
		}
	}
	if t, err := hdr.Date(); err == nil {
		m.Date = t.UTC()
	}
	return m
}

func indexBlankLine(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\n' && (b[i+1] == '\n' || (i+2 < len(b) && b[i+1] == '\r' && b[i+2] == '\n')) {
			return i + 1
		}
	}
	return -1
}
