package protocol

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A turn's activity reaches the room unchanged (wire.JobActivity): its
// kinds and bounds are the same values.
func TestTurnActivityIsTheWires(t *testing.T) {
	if TurnActivityTool != wire.JobActivityTool || TurnActivityThinking != wire.JobActivityThinking || TurnActivityText != wire.JobActivityText {
		t.Fatal("turn activity kinds differ from wire.JobActivity kinds")
	}
	if MaxTurnActivity != wire.MaxJobActivity || MaxTurnActivityTitle != wire.MaxJobActivityTitle {
		t.Fatal("turn activity bounds differ from wire's")
	}
}
