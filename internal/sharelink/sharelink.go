// Package sharelink is the one owner of a share link's token and URL
// (docs/prd/2026-10-13-short-share-links.md): it mints the token, says
// whether a string is one, hashes it for storage and lookup, and spells the
// link (`<public origin>/s/<token>`) and the share page it opens
// (`/#/share/<token>`). Nothing else spells either
// (TestShareLinkSpelledOnce).
//
// A token is TokenLen base62 characters from a CSPRNG (about 131 bits),
// with no id part: the room service stores only Hash(token) and finds the
// link by it.
package sharelink

import (
	"crypto/rand"

	"github.com/luckylhb90/bnbloc/internal/digest"
)

// TokenLen is a token's length: 22 base62 characters, 22·log2(62) ≈ 131
// bits.
const TokenLen = 22

// alphabet is base62, the characters a token is made of: safe in a path, a
// fragment and a chat message without escaping.
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Prefix is the gateway path a link opens at (request logs redact what
// follows it).
const Prefix = "/s/"

// Pattern is the gateway route of a link (its {token} path value is the
// token).
const Pattern = "GET " + Prefix + "{token}"

// New mints a token and the hash to store (the token itself is shown once
// and never stored).
func New() (token, hash string) {
	b := make([]byte, 0, TokenLen)
	var buf [32]byte
	for len(b) < TokenLen {
		// crypto/rand.Read never returns an error (Go 1.24+: it aborts
		// instead).
		_, _ = rand.Read(buf[:])
		for _, c := range buf {
			// Rejection sampling keeps every character equally likely:
			// 248 = 4·62 is the largest multiple of 62 a byte holds.
			if c < 248 && len(b) < TokenLen {
				b = append(b, alphabet[c%62])
			}
		}
	}
	token = string(b)
	return token, Hash(token)
}

// Valid reports whether s is a token: exactly TokenLen base62 characters.
// Anything else (an older link's token included) names no link.
func Valid(s string) bool {
	if len(s) != TokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z') {
			return false
		}
	}
	return true
}

// Hash is what the store keeps of a token and finds the link by: its
// sha256 (digest.Of).
func Hash(token string) string { return digest.Of([]byte(token)) }

// Path is a link's path on the gateway: "/s/<token>".
func Path(token string) string { return Prefix + token }

// URL is the link handed out: the gateway's public origin (no trailing
// slash) and Path.
func URL(origin, token string) string { return origin + Path(token) }

// PagePath is where the gateway sends a link: the SPA's share page,
// "/#/share/<token>" (the web's hashRoute parses it, and its sign-in
// return path keeps it).
func PagePath(token string) string { return "/#/share/" + token }
