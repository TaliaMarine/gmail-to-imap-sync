package crypto

import (
	"strings"
	"testing"
)

func mustRing(t *testing.T, ids ...string) (*Ring, string) {
	t.Helper()
	var parts []string
	for _, id := range ids {
		s, err := GenerateRingSpec(id)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, s)
	}
	spec := strings.Join(parts, ",")
	r, err := ParseRing(spec)
	if err != nil {
		t.Fatal(err)
	}
	return r, spec
}

func TestRoundTripAndBinding(t *testing.T) {
	r, _ := mustRing(t, "k1")
	ct, err := r.Encrypt([]byte("refresh-token"), "accounts.token.1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "refresh-token") {
		t.Fatal("plaintext leaked")
	}
	pt, err := r.Decrypt(ct, "accounts.token.1")
	if err != nil || string(pt) != "refresh-token" {
		t.Fatalf("Decrypt = %q, %v", pt, err)
	}
	if _, err := r.Decrypt(ct, "accounts.token.2"); err == nil {
		t.Error("ciphertext decrypted in another context")
	}
	if _, err := r.Decrypt(ct[:len(ct)-2], "accounts.token.1"); err == nil {
		t.Error("truncated ciphertext accepted")
	}
	ct2, _ := r.Encrypt([]byte("refresh-token"), "accounts.token.1")
	if ct == ct2 {
		t.Error("nonce reused")
	}
}

func TestRotation(t *testing.T) {
	old, oldSpec := mustRing(t, "old")
	ct, _ := old.Encrypt([]byte("x"), "c")

	newSpec, _ := GenerateRingSpec("new")
	r, err := ParseRing(newSpec + "," + oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	if r.ActiveKeyID() != "new" || !r.NeedsRotation(ct) {
		t.Fatal("expected old ciphertext to need rotation")
	}
	pt, err := r.Decrypt(ct, "c")
	if err != nil || string(pt) != "x" {
		t.Fatalf("old key must still decrypt: %q %v", pt, err)
	}
	re, _ := r.Encrypt(pt, "c")
	if r.NeedsRotation(re) {
		t.Error("re-encrypted value should use the active key")
	}
}

func TestParseRingErrors(t *testing.T) {
	for _, spec := range []string{"", "nokey", "a:!!!", "a:AAAA", ":AAAA"} {
		if _, err := ParseRing(spec); err == nil {
			t.Errorf("ParseRing(%q) accepted", spec)
		}
	}
	s, _ := GenerateRingSpec("a")
	if _, err := ParseRing(s + "," + s); err == nil {
		t.Error("duplicate id accepted")
	}
}
