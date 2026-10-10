package pkgmeta

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

type minisignSignature struct {
	KeyID     string
	Signature []byte
}

func DecodeMinisignPublicKey(raw string) (string, ed25519.PublicKey, error) {
	encoded, ok := firstBase64Line(raw)
	if !ok {
		return "", nil, fmt.Errorf("public key is not base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("public key is not base64: %w", err)
	}
	if len(decoded) != 42 || string(decoded[:2]) != "Ed" {
		return "", nil, fmt.Errorf("public key must be minisign Ed25519 format")
	}
	return hex.EncodeToString(decoded[2:10]), ed25519.PublicKey(decoded[10:42]), nil
}

func decodeMinisignSignature(raw []byte) (minisignSignature, error) {
	encoded, ok := firstBase64Line(string(raw))
	if !ok {
		return minisignSignature{}, fmt.Errorf("signature must be minisign Ed25519 detached format")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return minisignSignature{}, fmt.Errorf("signature is not base64: %w", err)
	}
	if len(decoded) == 74 && string(decoded[:2]) == "Ed" {
		return minisignSignature{KeyID: hex.EncodeToString(decoded[2:10]), Signature: decoded[10:74]}, nil
	}
	return minisignSignature{}, fmt.Errorf("signature must be minisign Ed25519 detached format")
}

func trustedPublicKey(keyID string, trusted []TrustedKey) (ed25519.PublicKey, error) {
	for _, key := range trusted {
		trustedID, pub, err := DecodeMinisignPublicKey(key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("trusted key %s invalid: %w", key.KeyID, err)
		}
		if strings.EqualFold(keyID, trustedID) && (key.KeyID == "" || strings.EqualFold(key.KeyID, trustedID)) {
			return pub, nil
		}
	}
	return nil, fmt.Errorf("untrusted publisher key %s", keyID)
}

func firstBase64Line(raw string) (string, bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "untrusted comment:") || strings.HasPrefix(line, "trusted comment:") {
			continue
		}
		return line, true
	}
	return "", false
}
