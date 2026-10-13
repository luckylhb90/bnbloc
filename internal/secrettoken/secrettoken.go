// Package secrettoken mints and verifies the "<prefix><id>_<secret>" bearer
// credentials bnbloc hands out once and stores only as a hash: API tokens
// (bnbloc_at_). The id is a random hex
// lookup key; the secret is 32 random bytes, base64url, and only its sha256
// hex is persisted, so a database read never yields a usable credential.
// Verification is constant-time.
package secrettoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// secretBytes is the random secret length (256 bits).
const secretBytes = 32

// Format is one token family: its prefix and the hex length of its id.
type Format struct {
	// Prefix starts every token of the family ("bnbloc_at_").
	Prefix string
	// IDLen is the hex id length (even; 32 = 128 bits).
	IDLen int
}

// New mints a token: its id, the full token string (shown once), and the
// hash of its secret (the only part to persist).
func (f Format) New() (id, token, secretHash string, err error) {
	idBuf := make([]byte, f.IDLen/2)
	secret := make([]byte, secretBytes)
	if _, err = rand.Read(idBuf); err != nil {
		return "", "", "", err
	}
	if _, err = rand.Read(secret); err != nil {
		return "", "", "", err
	}
	id = hex.EncodeToString(idBuf)
	sec := base64.RawURLEncoding.EncodeToString(secret)
	return id, f.Prefix + id + "_" + sec, HashSecret(sec), nil
}

// Parse splits "<prefix><id>_<secret>"; ok=false for any other shape.
func (f Format) Parse(raw string) (id, secret string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(raw), f.Prefix)
	if !found || len(rest) < f.IDLen+2 || rest[f.IDLen] != '_' {
		return "", "", false
	}
	id, secret = rest[:f.IDLen], rest[f.IDLen+1:]
	if _, err := hex.DecodeString(id); err != nil || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

// HashSecret is the persisted form of a secret: sha256, lowercase hex.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether secret hashes to storedHash, in constant time.
func Verify(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(storedHash)) == 1
}
