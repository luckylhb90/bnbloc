package pulse

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// MinKeyLen is the shortest key MemberHash accepts (SHA-256 block-sized keys
// are the usual choice; anything shorter is a configuration mistake).
const MinKeyLen = 32

// ErrShortKey means the Pulse member key is too short to use.
var ErrShortKey = errors.New("pulse: member key shorter than 32 bytes")

// MemberHash turns a user id into the keyed digest an event may carry. The key
// lives in the platform's configuration, never in Pulse's storage, so a dump of
// Pulse's state cannot be matched back to members without it.
func MemberHash(key []byte, userID string) (string, error) {
	if len(key) < MinKeyLen {
		return "", ErrShortKey
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil)), nil
}
