package room

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// The one room-creation and member-add paths. POST /v1/rooms, a handoff
// import, and a template instantiation all create rooms through createRoom;
// POST .../members and a template's default members both add through
// addMember, so their invariants (default channel created atomically, owner
// never granted or changed by an add) hold for every caller.

// createRoom atomically creates room with owner as its owner and its default
// channel (named defaultChannel, "general" when empty). It returns the
// default channel. store.ErrRoomExists when the id is taken in any tenant.
func (s *Service) createRoom(ctx context.Context, room store.Room, owner, defaultChannel string) (store.RoomChannel, error) {
	if strings.TrimSpace(defaultChannel) == "" {
		defaultChannel = "general"
	}
	room.CreatedBy = owner
	def := store.RoomChannel{
		ChannelID:   ids.New("ch_"),
		TenantID:    tenant.FromContext(ctx),
		RoomID:      room.RoomID,
		Name:        defaultChannel,
		IsDefault:   true,
		CreatedBy:   owner,
		CreatedAtMs: room.CreatedAtMs,
	}
	// The default channel is created in the same transaction as the room
	// (design lock): a room without its default channel is an undefined state,
	// so a failed channel insert fails the room creation itself.
	m := store.Membership{RoomID: room.RoomID, UserID: owner, Role: store.RoleOwner, JoinedAtMs: room.CreatedAtMs}
	if err := s.Store.CreateRoomWithOwnerAndChannel(ctx, room, m, def); err != nil {
		return store.RoomChannel{}, err
	}
	return def, nil
}

// errMemberPolicy is an add-member request the role policy refuses; status is
// the HTTP status for it.
type errMemberPolicy struct {
	status int
	msg    string
}

func (e errMemberPolicy) Error() string { return e.msg }

// addMember adds (or re-roles) userID in roomID. The caller has already
// checked that the actor may add members. The owner's role is immutable here
// (ownership moves only by transfer), and owner is never granted, so a room
// can never end up with two owners.
func (s *Service) addMember(ctx context.Context, roomID, userID string, role store.Role) error {
	if strings.TrimSpace(userID) == "" {
		return errMemberPolicy{http.StatusBadRequest, "userId is required"}
	}
	if existing, ok := s.Store.MemberRole(ctx, roomID, userID); ok && existing == store.RoleOwner {
		return errMemberPolicy{http.StatusForbidden, "cannot change the owner's role; transfer ownership instead"}
	}
	if role == store.RoleOwner {
		return errMemberPolicy{http.StatusBadRequest, "use ownership transfer to grant owner"}
	}
	return s.Store.AddMember(ctx, store.Membership{RoomID: roomID, UserID: userID, Role: role, JoinedAtMs: s.Now()})
}

// writeMemberErr maps an addMember error to its HTTP status.
func writeMemberErr(w http.ResponseWriter, err error) {
	var pol errMemberPolicy
	if errors.As(err, &pol) {
		writeErr(w, pol.status, pol.msg)
		return
	}
	obs.ServerError(w, "add member failed", err)
	writeErr(w, http.StatusInternalServerError, err.Error())
}
