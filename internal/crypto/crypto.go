// Package crypto encrypts secrets at rest (OAuth tokens, IMAP passwords) with
// AES-256-GCM. Ciphertexts carry the id of the key that produced them so keys
// can be rotated: the first key of the ring encrypts, any key decrypts.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const keySize = 32

// Ring holds the encryption keys.
type Ring struct {
	activeID string
	keys     map[string]cipher.AEAD
}

// ParseRing parses "id:base64key,id:base64key". The first key is active.
func ParseRing(spec string) (*Ring, error) {
	r := &Ring{keys: map[string]cipher.AEAD{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, enc, ok := strings.Cut(part, ":")
		if !ok || id == "" || strings.ContainsAny(id, ".:,") {
			return nil, errors.New("encryption key must look like id:base64")
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			if raw, err = base64.RawURLEncoding.DecodeString(enc); err != nil {
				return nil, fmt.Errorf("key %q: not base64", id)
			}
		}
		if len(raw) != keySize {
			return nil, fmt.Errorf("key %q must be %d bytes, got %d", id, keySize, len(raw))
		}
		if _, dup := r.keys[id]; dup {
			return nil, fmt.Errorf("duplicate key id %q", id)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		if r.activeID == "" {
			r.activeID = id
		}
		r.keys[id] = gcm
	}
	if r.activeID == "" {
		return nil, errors.New("no encryption keys configured")
	}
	return r, nil
}

// GenerateRingSpec returns a ring spec with one fresh key.
func GenerateRingSpec(id string) (string, error) {
	b := make([]byte, keySize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return id + ":" + base64.StdEncoding.EncodeToString(b), nil
}

// ActiveKeyID is the id of the key used for new ciphertexts.
func (r *Ring) ActiveKeyID() string { return r.activeID }

// Encrypt returns "keyid.base64(nonce||ciphertext)". context binds the
// ciphertext to its location (for example "accounts.refresh_token.<uuid>") so
// a value copied to another row or column fails to decrypt.
func (r *Ring) Encrypt(plaintext []byte, context string) (string, error) {
	aead := r.keys[r.activeID]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, []byte(context))
	return r.activeID + "." + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (r *Ring) Decrypt(value, context string) ([]byte, error) {
	id, enc, ok := strings.Cut(value, ".")
	if !ok {
		return nil, errors.New("malformed ciphertext")
	}
	aead, ok := r.keys[id]
	if !ok {
		return nil, fmt.Errorf("unknown encryption key %q", id)
	}
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, errors.New("malformed ciphertext")
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, []byte(context))
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	return pt, nil
}

// NeedsRotation reports whether value was encrypted with a non-active key.
func (r *Ring) NeedsRotation(value string) bool {
	id, _, _ := strings.Cut(value, ".")
	return id != r.activeID
}
