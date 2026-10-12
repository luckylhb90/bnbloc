// Package sealbox seals secrets bnbloc must store and later hand back in
// clear (an organization's MCP tool connector credentials), as opposed to
// secrets it only verifies (internal/secrettoken keeps a hash). A Box is
// AES-256-GCM under one deployment key; every sealed value is bound to the
// additional data the caller names (its storage key), so a sealed value
// copied to another row does not open.
//
// The key is BNBLOC_SECRET_STORE_KEY (32 random bytes, base64 or hex). It is
// optional: without it a Box is nil and features that store such secrets
// are off. It is never derived from another key, so rotating the internal
// or session key never makes stored secrets unreadable.
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeyEnv names the deployment key.
const KeyEnv = "BNBLOC_SECRET_STORE_KEY"

// version prefixes every sealed value, so a future scheme is a new prefix.
const version = "v1:"

// ErrOpen reports a value that does not open under this key and additional
// data (tampered, moved, or sealed under another key).
var ErrOpen = errors.New("sealed value does not open")

// Box seals and opens values under one key.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes (base64 or hex), got %d", KeyEnv, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// FromEnv builds the deployment's Box: nil (and no error) when the key is
// unset, an error when it is set but malformed (fatal at startup).
func FromEnv() (*Box, error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, nil
	}
	key, err := decodeKey(raw)
	if err != nil {
		return nil, err
	}
	return New(key)
}

func decodeKey(raw string) ([]byte, error) {
	if len(raw) == 64 {
		if key, err := hex.DecodeString(raw); err == nil {
			return key, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(raw); err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("%s must be 32 bytes, base64 or hex encoded", KeyEnv)
}

// Seal encrypts plaintext bound to ad.
func (b *Box) Seal(plaintext []byte, ad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plaintext, []byte(ad))
	return version + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a Seal result bound to ad.
func (b *Box) Open(sealed, ad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, version)
	if !ok {
		return nil, ErrOpen
	}
	raw, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return nil, ErrOpen
	}
	nonce, ct := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	out, err := b.aead.Open(nil, nonce, ct, []byte(ad))
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}
