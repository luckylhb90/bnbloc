package realtime

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The published-event metric labels are the registered kinds: a new kind
// is labelled as itself without a second list, anything else is "other".
func TestEventKindLabelsDeriveFromRegistry(t *testing.T) {
	for _, k := range wire.RoomEventKinds {
		if got := EventKindLabel(k.Name); got != k.Name {
			t.Errorf("label(%s) = %q", k.Name, got)
		}
	}
	if got := EventKindLabel("made_up_kind"); got != "other" {
		t.Errorf("label(unregistered) = %q, want other", got)
	}
}
