package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Sealer signs small JSON values for use in cookies ("base64(payload).base64(hmac)").
// Each purpose gets its own derived key, so a value sealed for one purpose
// (say the Google OAuth state) is useless for another (the login session).
// The value is signed, not encrypted: do not put secrets in it.
type Sealer struct{ key []byte }

// NewSealer derives a signing key for purpose from the application key.
func NewSealer(appKey []byte, purpose string) *Sealer {
	mac := hmac.New(sha256.New, appKey)
	mac.Write([]byte("sealer:" + purpose))
	return &Sealer{key: mac.Sum(nil)}
}

// Seal serialises and signs v.
func (s *Sealer) Seal(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload)), nil
}

// Open verifies the signature of value and decodes it into v.
func (s *Sealer) Open(value string, v any) error {
	p, sig, ok := strings.Cut(value, ".")
	if !ok {
		return errors.New("malformed value")
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !hmac.Equal(got, s.sign(payload)) {
		return errors.New("invalid signature")
	}
	return json.Unmarshal(payload, v)
}

func (s *Sealer) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return mac.Sum(nil)
}
