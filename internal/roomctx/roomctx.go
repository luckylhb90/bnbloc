// Package roomctx enforces the roomId ≡ workspaceId invariant (RFC-hard):
// the room scope identifier equals the member daemon's workspaceId with no
// implicit translation anywhere in the control-plane.
package roomctx

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrScopeMismatch is returned when a report's workspaceId does not equal the
// room scope it was posted to. bnbloc never remaps between the two.
var ErrScopeMismatch = errors.New("roomctx: workspaceId does not equal roomId scope")

// ErrEmptyScope is returned when a room/workspace identifier is empty.
var ErrEmptyScope = errors.New("roomctx: empty room scope")

// Normalize trims a room/workspace identifier and rejects an empty value.
func Normalize(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", ErrEmptyScope
	}
	return trimmed, nil
}

// RequireEqual verifies that a report's workspaceId equals the room scope it is
// being written under. This is the single enforcement point for the invariant.
func RequireEqual(roomID, workspaceID string) (string, error) {
	room, err := Normalize(roomID)
	if err != nil {
		return "", err
	}
	ws, err := Normalize(workspaceID)
	if err != nil {
		return "", err
	}
	if room != ws {
		return "", ErrScopeMismatch
	}
	return room, nil
}

// MaxIDBytes is the longest room id the control plane stores (room_id columns
// are VARCHAR(64)).
const MaxIDBytes = 64

// ErrInvalidID is returned for a room id that cannot be stored or routed.
var ErrInvalidID = errors.New("roomctx: invalid room id")

// ValidateID enforces the room id shape every service can store and route: 1 to
// MaxIDBytes bytes of valid UTF-8, no surrounding whitespace, no control
// characters, and none of the URL-structural characters / \ ? # % — a room id
// is a single path segment end to end (gateway routing, backend mux, bridge
// URLs), so it must never be able to smuggle a path separator or escape.
func ValidateID(id string) error {
	if id == "" || len(id) > MaxIDBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return ErrInvalidID
	}
	for _, r := range id {
		if unicode.IsControl(r) || strings.ContainsRune(`/\?#%`, r) {
			return ErrInvalidID
		}
	}
	return nil
}
