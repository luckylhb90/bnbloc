package room

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The one room-creation and join paths. POST /v1/rooms, a handoff import,
// and a template instantiation all create rooms through createRoom; every
// other membership a person gains (POST .../members, a template's default
// members, joining a shared room, redeeming an invite) goes through
// addMember or redeemInvite, so their invariants (default channel created
// atomically, owner never granted or changed by an add) hold for every
// caller and each join announces member_joined (TestJoinsAnnounced).

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
	prev, had := s.Store.GetMember(ctx, roomID, userID)
	if err := s.Store.AddMember(ctx, store.Membership{RoomID: roomID, UserID: userID, Role: role, JoinedAtMs: s.Now()}); err != nil {
		return err
	}
	if !had || prev.AgentHost {
		s.publishMemberJoined(ctx, roomID, userID)
	}
	return nil
}

// redeemInvite redeems an invite code for userID and announces the join.
// A redeem by someone already in the room announces it again; what listens
// is idempotent (an existing copy is left unchanged).
func (s *Service) redeemInvite(ctx context.Context, code, userID string) (store.Membership, error) {
	m, err := s.Store.RedeemInvite(ctx, code, userID, s.Now())
	if err != nil {
		return m, err
	}
	s.publishMemberJoined(ctx, m.RoomID, m.UserID)
	return m, nil
}

// publishMemberJoined announces that userID became a (human) member of
// roomID: a re-role is not a join, an agent host turned member is.
// Realtime hands the new member the room's standing content shares.
func (s *Service) publishMemberJoined(ctx context.Context, roomID, userID string) {
	if s.Publisher == nil {
		return
	}
	s.Publisher.Publish(ctx, wire.Event{
		RoomID:   roomID,
		Kind:     wire.EventMemberJoined,
		Payload:  wire.MemberJoinedPayload{UserID: userID},
		AtUnixMs: s.Now(),
	})
}

// writeMemberErr maps an addMember error to its HTTP status.
func writeMemberErr(w http.ResponseWriter, err error) {
	var pol errMemberPolicy
	if errors.As(err, &pol) {
		httpx.WriteStatus(w, pol.status, pol.msg, err)
		return
	}
	obs.ServerError(w, "add member failed", err)
	httpx.Error(w, err.Error(), http.StatusInternalServerError)
}
