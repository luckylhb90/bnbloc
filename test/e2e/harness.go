package e2e

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/store"
	roomsvc "github.com/luckylhb90/bnbloc/services/room"
)

// newRoomHandler builds the bnbloc-room handler over a shared room store; its
// channel store is the view over the same storage so the default channel
// created with every room is visible.
func newRoomHandler(rooms *store.MemoryRoom) http.Handler {
	svc := roomsvc.New(rooms, rooms.Channels())
	svc.Artifacts = store.NewMemoryArtifacts()
	return svc.Handler()
}
