package auth

import "testing"

func TestSealerRoundTripAndPurposeSeparation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	a, b := NewSealer(key, "oauth-state"), NewSealer(key, "other")
	v, err := a.Seal(map[string]string{"state": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := a.Open(v, &out); err != nil || out["state"] != "s1" {
		t.Fatalf("open = %v, %v", out, err)
	}
	if err := b.Open(v, &out); err == nil {
		t.Error("value opened under another purpose")
	}
	if err := a.Open("x"+v, &out); err == nil {
		t.Error("tampered value accepted")
	}
	if err := NewSealer([]byte("another key another key another!"), "oauth-state").Open(v, &out); err == nil {
		t.Error("value opened with another key")
	}
}
