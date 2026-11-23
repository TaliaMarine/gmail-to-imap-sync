package imapdest

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAddHeaderPrependsAndKeepsEveryOtherByte(t *testing.T) {
	cases := map[string]string{
		"crlf":        "From: a@x\r\nSubject: hi\r\n\r\nbody\r\n",
		"lf only":     "From: a@x\nSubject: hi\n\nbody\n",
		"8bit body":   "From: a@x\r\n\r\nH\xe9llo \xff\r\n",
		"no body":     "From: a@x\r\n",
		"no headers":  "\r\nbody only",
		"empty":       "",
		"folded":      "Subject: one\r\n two\r\n\r\nbody",
		"trailing ws": "From: a@x\r\n\r\nbody  \r\n\r\n",
	}
	for name, in := range cases {
		out, err := AddHeader([]byte(in), "acct.m1")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := "X-Gmail-Archive-ID: acct.m1\r\n" + in
		if string(out) != want {
			t.Errorf("%s:\n got %q\nwant %q", name, out, want)
		}
	}
}

func TestAddHeaderIsIdempotent(t *testing.T) {
	in := []byte("From: a@x\r\n\r\nbody\r\n")
	once, _ := AddHeader(in, "acct.m1")
	twice, _ := AddHeader(once, "acct.m1")
	if !bytes.Equal(once, twice) {
		t.Errorf("header added twice:\n%q", twice)
	}
	if strings.Count(string(twice), HeaderName) != 1 {
		t.Error("expected exactly one header")
	}
}

func TestAddHeaderOnlyCountsRealHeaders(t *testing.T) {
	// The same text in the body is not a header: ours must still be added.
	in := []byte("From: a@x\r\n\r\nX-Gmail-Archive-ID: acct.m1\r\n")
	out, _ := AddHeader(in, "acct.m1")
	if !bytes.HasPrefix(out, []byte("X-Gmail-Archive-ID: acct.m1\r\nFrom:")) {
		t.Errorf("header missing: %q", out)
	}
	// A header with a different value (a forwarded archive copy) does not
	// stop ours from being added, and the foreign one is preserved.
	in = []byte("X-Gmail-Archive-ID: other.m9\r\nFrom: a@x\r\n\r\nbody")
	out, _ = AddHeader(in, "acct.m1")
	if !bytes.HasPrefix(out, []byte("X-Gmail-Archive-ID: acct.m1\r\nX-Gmail-Archive-ID: other.m9\r\n")) {
		t.Errorf("out = %q", out)
	}
	if got := headerValues(headerBlock(out)); len(got) != 2 || got[0] != "acct.m1" {
		t.Errorf("values = %v", got)
	}
}

func TestHeaderValuesParsesFetchResponses(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"X-Gmail-Archive-ID: a.1\r\n\r\n", []string{"a.1"}},
		{"x-gmail-archive-id:   a.1  \r\n\r\n", []string{"a.1"}},
		{"X-Gmail-Archive-ID: a.1\r\nX-Gmail-Archive-ID: b.2\r\n\r\n", []string{"a.1", "b.2"}},
		{"X-Gmail-Archive-ID:\r\n a.1\r\n\r\n", []string{"a.1"}}, // folded
		{"\r\n", nil}, // message without the header: empty section
		{"", nil},
	}
	for _, tc := range cases {
		got := headerValues([]byte(tc.in))
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("headerValues(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHeaderValue(t *testing.T) {
	if HeaderValue("acct", "18c0ffee") != "acct.18c0ffee" {
		t.Error("unexpected header value")
	}
}

func FuzzAddHeader(f *testing.F) {
	for _, s := range []string{"", "\n", "\r\n\r\n", "a: b\r\n\r\nc", "X-Gmail-Archive-ID: v\n\n", "no colon\n\nx"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := AddHeader(raw, "acct.m1")
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if !bytes.HasSuffix(out, raw) {
			t.Fatal("original bytes not preserved at the end")
		}
		again, err := AddHeader(out, "acct.m1")
		if err != nil || !bytes.Equal(again, out) {
			t.Fatal("not idempotent")
		}
		if v := headerValues(headerBlock(out)); len(v) == 0 || v[0] != "acct.m1" {
			t.Fatalf("our header is not read back first: %v", v)
		}
		if len(out) != len(raw) && !bytes.HasPrefix(out, []byte(HeaderName+": acct.m1\r\n")) {
			t.Fatal("header not first")
		}
	})
}

func TestAddHeaderRejectsLeadingWhitespace(t *testing.T) {
	for _, in := range []string{" folded", "\tfolded"} {
		if _, err := AddHeader([]byte(in), "acct.m1"); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}
