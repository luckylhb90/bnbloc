package store

import (
	"context"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Who may read a channel (docs/prd/2026-10-28-channels-and-threads.md
// §2.1a, C11-C14): the ONE rule, MayReadChannel, and what it reads. Every
// path that shows a channel or its content asks it (the channel listing and
// directory, the room stream, history and revisions, search, mention
// notifications and plugin hooks, scheduled messages, message tasks,
// deliverables by channel, unread counts, the welcome page, the agents'
// records through the bridge's listing); TestChannelReadsAskTheRule
// (services) fails when a route reads channel content without it.

// SectionMember is one member of a members-only section (C12, C17): who may
// read its channels. AddedBy is who let them in (themselves for an open join
// or a request approved by an owner: the approver).
type SectionMember struct {
	TenantID   string
	RoomID     string
	SectionID  string
	UserID     string
	AddedBy    string
	JoinedAtMs int64
}

// SectionJoinRequest is a member's open request to join an "ask" section
// (C17): its owners approve or decline it.
type SectionJoinRequest struct {
	TenantID      string
	RoomID        string
	SectionID     string
	UserID        string
	RequestedAtMs int64
}

// Section join policies (C17): who may join a members-only section.
const (
	SectionJoinInvite = wire.SectionJoinInvite
	SectionJoinAsk    = wire.SectionJoinAsk
	SectionJoinOpen   = wire.SectionJoinOpen
)

// ErrNotSectionMember is returned by RemoveSectionMember for a user who is
// not a member; ErrNoJoinRequest by TakeSectionJoinRequest for a request
// that is not open.
var (
	ErrNotSectionMember = errors.New("store: not a member of the section")
	ErrNoJoinRequest    = errors.New("store: no open request to join the section")
)

// SectionMembershipStore persists who belongs to the room's members-only
// sections and who asked to join. Implementations scope every operation by
// the context's tenant (fail closed without one). The rows of a deleted
// section are left (a deleted section governs no channel any more); a room's
// rows go with the room.
type SectionMembershipStore interface {
	// AddSectionMember makes userID a member of the section (a repeat keeps
	// the first time and who let them in) and closes their join request.
	AddSectionMember(ctx context.Context, m SectionMember) error
	// RemoveSectionMember ends userID's membership; ErrNotSectionMember when
	// they are none.
	RemoveSectionMember(ctx context.Context, roomID, sectionID, userID string) error
	// SectionMembers returns a section's members, oldest first.
	SectionMembers(ctx context.Context, roomID, sectionID string) ([]SectionMember, error)
	// MemberSections returns the ids of the sections of the room userID
	// belongs to.
	MemberSections(ctx context.Context, roomID, userID string) ([]string, error)
	// PutSectionJoinRequest records userID's request (a repeat keeps the
	// first time).
	PutSectionJoinRequest(ctx context.Context, q SectionJoinRequest) error
	// SectionJoinRequests returns a section's open requests, oldest first.
	SectionJoinRequests(ctx context.Context, roomID, sectionID string) ([]SectionJoinRequest, error)
	// TakeSectionJoinRequest closes userID's open request;
	// ErrNoJoinRequest when there is none.
	TakeSectionJoinRequest(ctx context.Context, roomID, sectionID, userID string) error
}

// Reader is who a channel read is for (MayReadChannel): a person (a member
// of the room, or a previewer of a shared room) or an agent acting for one.
type Reader struct {
	// User is the person whose access decides: the member themself, or the
	// person an agent works for (its job's requester or a task's runner).
	// "" is nobody's: it reads no members-only channel.
	User string
	// Admin: the room's owner or an admin (C12: they see every section's
	// name and members, never its content without joining).
	Admin bool
	// Agent: the read is an agent's (ChannelAdmitsAgents applies too).
	Agent bool
}

// ChannelAccess is what MayReadChannel needs of a room: its sections and
// the reader's own section memberships. Load it once per request
// (LoadChannelAccess) and ask it for every channel.
type ChannelAccess struct {
	// membersOnly are the room's live members-only sections.
	membersOnly map[string]bool
	// mine are the members-only sections the reader belongs to.
	mine map[string]bool
	// reader is who the access was loaded for.
	reader Reader
}

// ChannelAccessReader is the reads LoadChannelAccess makes.
type ChannelAccessReader interface {
	ListSections(ctx context.Context, roomID string) ([]RoomSection, error)
	MemberSections(ctx context.Context, roomID, userID string) ([]string, error)
}

// LoadChannelAccess reads what MayReadChannel decides for reader in roomID:
// the room's members-only sections and, when there are any, the reader's
// memberships. A read failure is returned, never "may read" (every guard
// built on it fails closed). A nil store (in-memory dev without channels)
// has no members-only sections.
func LoadChannelAccess(ctx context.Context, r ChannelAccessReader, roomID string, reader Reader) (ChannelAccess, error) {
	a := ChannelAccess{membersOnly: map[string]bool{}, mine: map[string]bool{}, reader: reader}
	if r == nil {
		return a, nil
	}
	secs, err := r.ListSections(ctx, roomID)
	if err != nil {
		return a, err
	}
	for _, s := range secs {
		if s.MembersOnly() {
			a.membersOnly[s.SectionID] = true
		}
	}
	if len(a.membersOnly) == 0 || reader.User == "" {
		return a, nil
	}
	ids, err := r.MemberSections(ctx, roomID, reader.User)
	if err != nil {
		return a, err
	}
	for _, id := range ids {
		a.mine[id] = true
	}
	return a, nil
}

// MayReadChannel is the ONE rule of whether the access's reader may read c:
// a channel of an open section or the top level, or of a members-only
// section the reader (the person the read is for) belongs to; for an agent
// also ChannelAdmitsAgents' rule (never the people chat, never a channel
// barring agents). Room membership itself is the caller's check (RoomScope,
// requireReadAs): this decides only within the room. The room's owners and
// admins read a members-only section only by joining it (C12).
func (a ChannelAccess) MayReadChannel(c RoomChannel) bool {
	if a.reader.Agent && !c.Admission().Admits() {
		return false
	}
	return a.MaySeeSection(c.SectionID)
}

// MaySeeSection reports whether the reader may read the channels of
// sectionID ("" is the top level): an open or absent section, or a
// members-only one the reader belongs to.
func (a ChannelAccess) MaySeeSection(sectionID string) bool {
	return !a.membersOnly[sectionID] || a.mine[sectionID]
}

// MayKnowSection reports whether the reader may know sec exists (its name,
// C11, C12): anyone for an open section; a members-only one for its
// members, the room's owners and admins, and, when its owners show it for
// joining ("ask" or "open"), the room's people. An agent knows only the
// sections of the person it works for.
func (a ChannelAccess) MayKnowSection(sec RoomSection) bool {
	if !sec.MembersOnly() || a.mine[sec.SectionID] {
		return true
	}
	if a.reader.Agent || a.reader.User == "" {
		return false
	}
	return a.reader.Admin || sec.JoinPolicy == SectionJoinAsk || sec.JoinPolicy == SectionJoinOpen
}

// MayKnowMembers reports whether the reader may see who belongs to sec:
// its members and the room's owners and admins (C12), never someone who
// only sees its name for joining.
func (a ChannelAccess) MayKnowMembers(sec RoomSection) bool {
	return a.mine[sec.SectionID] || (a.reader.Admin && !a.reader.Agent)
}

// HasMembersOnly reports whether the room has a members-only section: a
// path that reads every channel may skip per-channel work when it has none.
func (a ChannelAccess) HasMembersOnly() bool { return len(a.membersOnly) > 0 }

// MembersOnly reports whether s is a members-only section (§2.1a).
func (s RoomSection) MembersOnly() bool { return s.Visibility == wire.SectionMembers }

// ChannelReadable is MayReadChannel for one channel by id: it loads the
// channel and the access, and answers found=false for no such live channel.
// A path that reads one channel asks it; a path that reads many loads the
// access once.
func ChannelReadable(ctx context.Context, r interface {
	ChannelAccessReader
	GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool, error)
}, roomID, channelID string, reader Reader) (may, found bool, err error) {
	c, ok, err := r.GetChannel(ctx, roomID, channelID)
	if err != nil || !ok {
		return false, ok, err
	}
	a, err := LoadChannelAccess(ctx, r, roomID, reader)
	if err != nil {
		return false, true, err
	}
	return a.MayReadChannel(c), true, nil
}
