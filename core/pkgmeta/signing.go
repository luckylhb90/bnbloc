package pkgmeta

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// GenerateSigningKey creates an Ed25519 keypair for signing core packages and
// returns the minisign-framed public key string (ready for a trusted-keys
// entry) plus the raw private key. The 8-byte key id is derived from the public
// key so a signature made with priv is attributable to this public key.
func GenerateSigningKey() (publicKeyMinisign string, keyID string, priv ed25519.PrivateKey, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", nil, err
	}
	id := deriveKeyID(pub)
	return EncodeMinisignPublicKey(pub, id), hex.EncodeToString(id), priv, nil
}

// EncodeMinisignPublicKey renders an Ed25519 public key as a minisign public-key
// line: base64("Ed" + 8-byte keyID + 32-byte public key). It is the inverse of
// DecodeMinisignPublicKey. keyID is the 8 raw bytes (as returned by
// deriveKeyID); a caller with a hex keyID should pass its decoded bytes.
func EncodeMinisignPublicKey(pub ed25519.PublicKey, keyID []byte) string {
	blob := make([]byte, 0, 42)
	blob = append(blob, 'E', 'd')
	blob = append(blob, keyID...)
	blob = append(blob, pub...)
	return base64.StdEncoding.EncodeToString(blob)
}

// SignPackage signs the exact package body bytes with priv and returns a
// minisign-framed detached signature: base64("Ed" + 8-byte keyID + 64-byte
// signature). It is the inverse of decodeMinisignSignature, so a signature made
// here verifies through VerifySignedPackage against the matching trusted public
// key. The keyID is derived from priv's public key so it matches the registered
// trusted key.
func SignPackage(body []byte, priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("pkgmeta: invalid ed25519 private key size %d", len(priv))
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("pkgmeta: private key has no ed25519 public key")
	}
	sig := ed25519.Sign(priv, body)
	blob := make([]byte, 0, 74)
	blob = append(blob, 'E', 'd')
	blob = append(blob, deriveKeyID(pub)...)
	blob = append(blob, sig...)
	return []byte(base64.StdEncoding.EncodeToString(blob)), nil
}

// deriveKeyID returns the 8-byte key id for a public key. It must be stable and
// collision-resistant enough to disambiguate trusted keys; the first 8 bytes of
// the public key serve (minisign uses a random id, but a public-key-derived id
// keeps sign/verify self-consistent without a separate id channel).
func deriveKeyID(pub ed25519.PublicKey) []byte {
	id := make([]byte, 8)
	copy(id, pub[:8])
	return id
}
