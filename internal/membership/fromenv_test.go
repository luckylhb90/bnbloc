package membership

import (
	"errors"
	"testing"
)

// FromEnv is the one membership constructor: the room service's client
// when its URL is set, never a silent empty set in production.
func TestFromEnv(t *testing.T) {
	t.Setenv(RoomBaseURLEnv, "")
	t.Setenv("BNBLOC_ENV", "production")
	if _, err := FromEnv(); !errors.Is(err, ErrRoomURLRequired) {
		t.Fatalf("production without the room URL: err = %v, want ErrRoomURLRequired", err)
	}
	t.Setenv(RoomBaseURLEnv, "http://room:8080")
	m, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if rc, ok := m.(*RoomClient); !ok || rc.BaseURL != "http://room:8080" {
		t.Fatalf("FromEnv = %#v, want the room client", m)
	}
	t.Setenv("BNBLOC_ENV", "")
	t.Setenv(RoomBaseURLEnv, "")
	m, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup("room-1", "maya"); ok {
		t.Fatal("the development directory must refuse, not admit")
	}
}
