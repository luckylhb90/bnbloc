// Package digest is the one owner of bnbloc's content digest rule: a sha256
// is 64 lower-case hex characters, and a content-addressed blob or artifact
// key is "sha256/<hex>". Every pin (bundle, artifact, site, blob key, MCP
// binary) is produced, parsed and compared through it, so there is one case
// rule: lower case only, compared exactly. An upper-case digest is not a
// digest (TestDigestRuleSingleOwner forbids a second copy).
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// KeyPrefix starts every content-addressed key.
const KeyPrefix = "sha256/"

// KeyPattern is the regular expression of a content-addressed key, for
// finding keys inside free text (artifact URLs held in other rows).
const KeyPattern = KeyPrefix + `[0-9a-f]{64}`

// Of returns the sha256 of data in the canonical form.
func Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Valid reports whether s is a canonical sha256: 64 lower-case hex
// characters.
func Valid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Key returns the content-addressed key of a canonical sha256.
func Key(sha string) string { return KeyPrefix + sha }

// ParseKey returns the sha256 a content-addressed key names; ok is false
// for anything but "sha256/" followed by a canonical sha256.
func ParseKey(key string) (sha string, ok bool) {
	sha, ok = strings.CutPrefix(key, KeyPrefix)
	if !ok || !Valid(sha) {
		return "", false
	}
	return sha, true
}

// ValidKey reports whether key is a content-addressed key.
func ValidKey(key string) bool {
	_, ok := ParseKey(key)
	return ok
}
