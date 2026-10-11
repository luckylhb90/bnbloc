// Package ids mints entity ids: a type prefix, then the creation time and
// randomness, as lowercase hex.
//
//	<prefix><12 hex: Unix milliseconds><20 hex: 80 random bits>
//
// The time part makes ids sort, and InnoDB insert them, in creation order
// (rows cluster by primary key, so random ids would insert at random
// positions); the random part keeps them unguessable and unique across
// replicas without coordination. Use it for entity primary keys, never for
// secrets (those stay fully random).
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New returns a fresh id with the given prefix (e.g. "art_").
func New(prefix string) string {
	return at(prefix, time.Now())
}

func at(prefix string, t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	// crypto/rand.Read never returns an error (Go 1.24+: it aborts instead).
	_, _ = rand.Read(b[6:])
	return prefix + hex.EncodeToString(b[:])
}
