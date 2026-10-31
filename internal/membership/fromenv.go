package membership

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/config"
)

// Service is bnbloc-room's membership surface as every other service uses
// it: the Directory request handlers check (RoomScope), the room listing
// global search filters on (MemberRooms), and the agent-host memberships
// shareagent converges. FromEnv is the one constructor; a service names the
// narrower interface it needs (Directory, Reader) on its own fields.
type Service interface {
	Directory
	ManyReader
	MemberRooms(ctx context.Context, userID, tenantID string) ([]RoomRef, error)
	MemberRoomIDs(ctx context.Context, userID, tenantID string) ([]string, error)
	PutAgentHost(ctx context.Context, tenantID, roomID, userID string) error
	DeleteAgentHost(ctx context.Context, tenantID, roomID, userID string) error
}

var (
	_ Service = (*RoomClient)(nil)
	_ Service = (*Static)(nil)
)

// RoomBaseURLEnv names bnbloc-room's base URL, the membership authority.
const RoomBaseURLEnv = "BNBLOC_ROOM_BASE_URL"

// ErrRoomURLRequired refuses to start a production service without the
// membership authority: an empty directory would silently refuse every
// member of every room.
var ErrRoomURLRequired = errors.New("membership: " + RoomBaseURLEnv + " is required when BNBLOC_ENV=production")

// FromEnv returns the membership service every backend (realtime, activity,
// shareagent and its notify consumers) and the gateway use: the room
// service's probe client (RoomClient) at BNBLOC_ROOM_BASE_URL. Unset, it is
// an error in production (ErrRoomURLRequired) and, in development, an empty
// in-memory directory announced with a warning: every room refuses every
// member until the room service is configured.
func FromEnv() (Service, error) {
	if url := strings.TrimSpace(config.FromEnvRaw(RoomBaseURLEnv)); url != "" {
		return NewRoomClient(url), nil
	}
	if config.Production() {
		return nil, ErrRoomURLRequired
	}
	log.Printf("membership: %s unset: an empty in-memory directory (development only); every room refuses every member", RoomBaseURLEnv)
	return NewStatic(), nil
}
