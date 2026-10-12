package store

import (
	"errors"
	"testing"
)

// The reindex walk's cursor round-trips and rejects cursors it did not issue.
func TestSearchCursorRoundTrip(t *testing.T) {
	for _, c := range []searchCursor{{roomID: "r1", seq: 7}, {roomID: "a/b", seq: 0}, {roomID: "x", seq: 1 << 40}} {
		got, err := parseSearchCursor(c.String())
		if err != nil || got != c {
			t.Fatalf("parse(%q) = %+v, %v; want %+v", c.String(), got, err, c)
		}
	}
	for _, bad := range []string{"r1", "/5", "r1/x", "r1/"} {
		if _, err := parseSearchCursor(bad); !errors.Is(err, ErrInvalidSearchCursor) {
			t.Fatalf("parse(%q) err = %v, want ErrInvalidSearchCursor", bad, err)
		}
	}
}
