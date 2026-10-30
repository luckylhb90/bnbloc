package room

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's channels and their setup (docs/architecture/room-channels.md;
// docs/prd/2026-10-28-channels-and-threads.md, slice 1):
//
//	GET    /v1/rooms/{roomId}/channels                     wire.ChannelList (?archived=1, ?agents=1)
//	GET    /v1/rooms/{roomId}/channel-directory            wire.ChannelDirectory
//	POST   /v1/rooms/{roomId}/channels                     wire.ChannelCreateRequest → wire.RoomChannel
//	PATCH  /v1/rooms/{roomId}/channels/{channelId}         wire.ChannelPatchRequest → wire.RoomChannel
//	POST   /v1/rooms/{roomId}/channels/{channelId}/archive → wire.RoomChannel
//	POST   /v1/rooms/{roomId}/channels/{channelId}/restore → wire.RoomChannel
//	DELETE /v1/rooms/{roomId}/channels/{channelId}
//	PUT    /v1/rooms/{roomId}/channels/{channelId}/follow  wire.ChannelFollowRequest → wire.RoomChannel
//	DELETE /v1/rooms/{roomId}/channels/{channelId}/follow  → wire.RoomChannel
//
// Every change publishes channel_updated (room-wide): clients reload the
// listing, and realtime's agent stream filters forget their verdicts.

// mayManageChannel is the ONE rule of who changes a channel's setup
// (rename, purpose, rules, section, position, owner, settings, archive,
// restore): the room's owner and admins, the channel's owner, and the owners
// of its section. decision: channels PRD C10 (and §2.1 "set by the room's
// owners and admins, and the channel's creator", whose owner it is by
// default, §2.1b).
func mayManageChannel(role store.Role, user string, ch store.RoomChannel, sec *store.RoomSection) bool {
	if role.CanInvite() {
		return true
	}
	if role == store.RoleVisitor {
		return false
	}
	return (ch.OwnerID != "" && ch.OwnerID == user) || (sec != nil && sec.IsOwner(user))
}

// mayPlaceChannel is the ONE rule of where a member may put a channel
// (creating it, or moving it): sec nil is the room's top level. The room's
// owner and admins place anywhere; visitors nowhere; anyone else by the
// room's creation policy. decision: channels PRD C15 (anyone at the top
// level or in a section they belong to; "sections" keeps the top level to
// owners and admins; "admins" keeps creation to them). Until a section has
// members (slice 2), a member belongs to an open section by being one of
// its owners (decision: pending owner; the narrowest reading of "a section
// they belong to").
func mayPlaceChannel(room store.Room, role store.Role, user string, sec *store.RoomSection) bool {
	if role.CanInvite() {
		return true
	}
	if role == store.RoleVisitor {
		return false
	}
	switch room.ChannelCreationPolicy() {
	case wire.ChannelCreationAnyone:
		return sec == nil || sec.IsOwner(user)
	case wire.ChannelCreationSections:
		return sec != nil && sec.IsOwner(user)
	}
	return false
}

// channelText trims a channel's purpose or rules and checks its bound (in
// characters).
func channelText(v *string, max int, what string) (string, error) {
	t := strings.TrimSpace(*v)
	if utf8.RuneCountInString(t) > max {
		return "", errBadRequest(what + " is too long (at most " + strconv.Itoa(max) + " characters)")
	}
	return t, nil
}

// errBadRequest is a refusal a route answers with its message.
type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }

// cleanChannelSettings validates a settings document.
func cleanChannelSettings(s wire.ChannelSettings) (wire.ChannelSettings, error) {
	if !wire.IsChannelWhoPosts(s.WhoPosts) {
		return s, errBadRequest("settings.whoPosts must be one of \"\" (everyone) or \"admins\"")
	}
	if !wire.IsChannelAgentParticipation(s.AgentParticipation) {
		return s, errBadRequest("settings.agentParticipation must be one of \"\" (when mentioned), \"questions\" or \"threads\"")
	}
	if s.SlowModeSeconds < 0 {
		return s, errBadRequest("settings.slowModeSeconds must not be negative (0 is off)")
	}
	if !wire.IsChannelPostingStyle(s.PostingStyle) {
		return s, errBadRequest("settings.postingStyle must be one of \"\" (messages) or \"threads\"")
	}
	if utf8.RuneCountInString(s.PostTemplate) > wire.MaxPostTemplate {
		return s, errBadRequest("settings.postTemplate is too long")
	}
	tags, err := cleanVocabulary(s.Tags, wire.MaxChannelTags, "tags", func(t wire.ChannelTag) (string, string) { return t.ID, t.Name })
	if err != nil {
		return s, err
	}
	for i := range tags {
		tags[i].Color = strings.TrimSpace(tags[i].Color)
		if len(tags[i].Color) > 16 {
			return s, errBadRequest("settings.tags: a color is a short name or #rrggbb")
		}
	}
	s.Tags = tags
	statuses, err := cleanVocabulary(s.Statuses, wire.MaxChannelStatuses, "statuses", func(t wire.ChannelStatus) (string, string) { return t.ID, t.Name })
	if err != nil {
		return s, err
	}
	s.Statuses = statuses
	if a := s.DefaultAgent; a != nil {
		// An agent is named by its key (owner, core, provider): a default
		// agent without an owner or a provider names none.
		cp := wire.ChannelAgent{OwnerID: strings.TrimSpace(a.OwnerID), Core: strings.TrimSpace(a.Core), Provider: strings.TrimSpace(a.Provider)}
		if cp.OwnerID == "" || cp.Provider == "" {
			return s, errBadRequest("settings.defaultAgent names its ownerId and provider")
		}
		s.DefaultAgent = &cp
	}
	return s, nil
}

// cleanVocabulary checks a forum's tags or statuses (C20): at most max, each
// a unique id (letters, digits, - and _, at most MaxChannelTagName) with a
// one-line name of 1–MaxChannelTagName characters. It returns them trimmed;
// nil for none.
func cleanVocabulary[T wire.ChannelTag | wire.ChannelStatus](in []T, max int, what string, parts func(T) (string, string)) ([]T, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > max {
		return nil, errBadRequest(fmt.Sprintf("settings.%s: at most %d", what, max))
	}
	seen := map[string]bool{}
	out := make([]T, 0, len(in))
	for _, v := range in {
		id, name := parts(v)
		id, name = strings.TrimSpace(id), strings.Join(strings.Fields(name), " ")
		if !wire.ValidVocabularyID(id) || seen[id] {
			return nil, errBadRequest(fmt.Sprintf("settings.%s: each needs a unique id of letters, digits, - or _", what))
		}
		if name == "" || utf8.RuneCountInString(name) > wire.MaxChannelTagName {
			return nil, errBadRequest(fmt.Sprintf("settings.%s: each needs a name of at most %d characters", what, wire.MaxChannelTagName))
		}
		seen[id] = true
		switch x := any(v).(type) {
		case wire.ChannelTag:
			x.ID, x.Name = id, name
			out = append(out, any(x).(T))
		case wire.ChannelStatus:
			x.ID, x.Name = id, name
			out = append(out, any(x).(T))
		}
	}
	return out, nil
}

// channelCaller is the caller of a channel route: their membership.
type channelCaller struct {
	user string
	role store.Role
}

// humanCaller reads the caller's membership: a human member (an agent
// host's is none). It answers the refusal itself.
func (s *Service) humanCaller(w http.ResponseWriter, r *http.Request, roomID string) (channelCaller, bool) {
	user := caller(r)
	m, ok, err := s.Store.GetMember(r.Context(), roomID, user)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return channelCaller{}, false
	}
	if !ok || !m.Human() {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return channelCaller{}, false
	}
	return channelCaller{user: user, role: m.Role}, true
}

// liveChannel reads a channel of the room and its section (nil at the top
// level), answering 404 itself.
func (s *Service) liveChannel(w http.ResponseWriter, r *http.Request, roomID string) (store.RoomChannel, *store.RoomSection, bool) {
	ch, ok, err := s.Channels.GetChannel(r.Context(), roomID, r.PathValue("channelId"))
	if err != nil {
		obs.ServerError(w, "read channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.RoomChannel{}, nil, false
	}
	if !ok {
		httpx.Error(w, "channel not found", http.StatusNotFound)
		return store.RoomChannel{}, nil, false
	}
	sec, err := s.sectionOf(r.Context(), roomID, ch.SectionID)
	if err != nil {
		obs.ServerError(w, "read section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.RoomChannel{}, nil, false
	}
	// A channel of a members-only section the caller is not in does not
	// exist for them (C11): 404, as for no such channel.
	access, ok := s.readerAccess(w, r, roomID)
	if !ok {
		return store.RoomChannel{}, nil, false
	}
	if !access.MayReadChannel(ch) {
		httpx.Error(w, "channel not found", http.StatusNotFound)
		return store.RoomChannel{}, nil, false
	}
	return ch, sec, true
}

// sectionOf reads a live section (nil for "" or a section that is gone: a
// deleted section's channels moved to the top level with it).
func (s *Service) sectionOf(ctx context.Context, roomID, sectionID string) (*store.RoomSection, error) {
	if sectionID == "" {
		return nil, nil
	}
	sec, ok, err := s.Channels.GetSection(ctx, roomID, sectionID)
	if err != nil || !ok {
		return nil, err
	}
	return &sec, nil
}

// channelView is the one projection of a channel for a caller (their
// following of it, f; zero for none). Every member is in the default
// channel: it cannot be left, so it is joined whether or not a follow was
// ever written (a room older than follows has none).
func channelView(c store.RoomChannel, f store.ChannelFollow) wire.RoomChannel {
	v := wire.RoomChannel{
		ChannelID: c.ChannelID, Name: c.Name, IsDefault: c.IsDefault, Kind: c.Kind, CreatedBy: c.CreatedBy,
		Purpose: c.Purpose, Rules: c.Rules, SectionID: c.SectionID, Position: c.Position, OwnerID: c.OwnerID,
		Settings: c.Settings, Joined: c.IsDefault || f.ChannelID != "", Starred: f.Starred, Hidden: f.Hidden, GroupID: f.GroupID,
		NotifyLevel: f.Level,
	}
	if c.ArchivedAtMs != nil {
		v.ArchivedAtMs = *c.ArchivedAtMs
	}
	if c.IsThread() {
		info := c.ThreadInfo()
		v.Joined = f.ChannelID != ""
		v.Thread = &info
	}
	v.NotifyEffective = wire.EffectiveNotifyLevel(f.Level, c.IsThread(), f.ChannelID != "", f.LastPostedAtMs > 0)
	return v
}

// sectionView is the one projection of a section.
func sectionView(sec store.RoomSection) wire.RoomSection {
	owners := slices.Clone(sec.Owners)
	if owners == nil {
		owners = []string{}
	}
	return wire.RoomSection{SectionID: sec.SectionID, Name: sec.Name, Position: sec.Position, Visibility: sec.Visibility,
		Rules: sec.Rules, Owners: owners, JoinPolicy: sec.JoinPolicy, CreatedBy: sec.CreatedBy, CreatedAtMs: sec.CreatedAtMs}
}

// followsOf reads user's follows in the room, by channel ("" user: none).
func (s *Service) followsOf(ctx context.Context, roomID, user string) (map[string]store.ChannelFollow, error) {
	out := map[string]store.ChannelFollow{}
	if user == "" {
		return out, nil
	}
	list, err := s.Channels.ChannelFollows(ctx, roomID, user)
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		out[f.ChannelID] = f
	}
	return out, nil
}

// viewFor answers one channel as the caller sees it.
func (s *Service) viewFor(w http.ResponseWriter, r *http.Request, roomID, channelID, user string) {
	ch, ok, err := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if err == nil && !ok {
		err = store.ErrChannelNotFound
	}
	var follows map[string]store.ChannelFollow
	if err == nil {
		follows, err = s.followsOf(r.Context(), roomID, user)
	}
	if err != nil {
		obs.ServerError(w, "read channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, channelView(ch, follows[channelID]))
}

// publishChannelUpdated announces a change to a channel or a section of the
// room (room-wide, wire.EventChannelUpdated).
func (s *Service) publishChannelUpdated(ctx context.Context, roomID, channelID, sectionID string) {
	if s.Publisher == nil {
		return
	}
	// A thread's change (its title, its archive) redraws its root's line
	// in the parent's stream; the listing has nothing to reload.
	if ch, found, err := s.Channels.GetChannel(ctx, roomID, channelID); err == nil && found && ch.IsThread() {
		s.publishThreadUpdated(ctx, ch)
		return
	}
	s.Publisher.Publish(ctx, wire.Event{
		RoomID: roomID, Kind: wire.EventChannelUpdated,
		Payload:  wire.ChannelUpdatedPayload{ChannelID: channelID, SectionID: sectionID},
		AtUnixMs: s.Now(),
	})
}

// handleListChannels returns a room's channels and sections. The caller is
// a member, or reads a shared room before joining it (preview.go). The
// people chat is never listed (the web reaches it through .../people-chat).
// By default the live channels that are not archived; ?archived=1 the
// archived ones (their history stays readable); ?agents=1 the channels an
// agent may read (store.ChannelAdmitsAgents' rule: no channel that bars
// agents; archived ones kept), which the bridge's records read
// (roomlog.AgentChannelList). Each channel carries the caller's own
// following (joined, starred, hidden).
func (s *Service) handleListChannels(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	member, ok := s.requireReadAs(w, r, roomID)
	if !ok {
		return
	}
	q := r.URL.Query()
	forAgents := q.Get(wire.ChannelListForAgents) == "1"
	archived := q.Get(wire.ChannelListArchived) == "1"
	// A room is created with its default channel in one transaction
	// (createRoom), so a read never has to mint one.
	channels, err := s.Channels.ListChannels(r.Context(), roomID)
	var sections []store.RoomSection
	if err == nil {
		sections, err = s.Channels.ListSections(r.Context(), roomID)
	}
	user := ""
	if member {
		user = caller(r)
	}
	var follows map[string]store.ChannelFollow
	if err == nil {
		follows, err = s.followsOf(r.Context(), roomID, user)
	}
	var groups []wire.ChannelGroup
	if err == nil {
		groups, err = s.groupsOf(r.Context(), roomID, user)
	}
	if err != nil {
		obs.ServerError(w, "list channels failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	access, ok := s.readerAccess(w, r, roomID)
	if !ok {
		return
	}
	out := wire.ChannelList{Channels: []wire.RoomChannel{}, Sections: []wire.RoomSection{}, Groups: groups}
	for _, c := range channels {
		switch {
		case !c.Listed():
			// The people chat and threads are no channels of the room's
			// list (threads: GET .../threads, under their parent).
			continue
		case forAgents:
			// decision: channels PRD §2.1 ("agents admitted ... a channel
			// agents never read") and C10 (an archived channel's history
			// stays readable, so an agent's records keep it). An agent
			// reads as the person it works for (C14): the reader asserted
			// with its job, else nobody's members-only section.
			if !c.Admission().Admits() || !access.MayReadChannel(c) {
				continue
			}
		case !access.MayReadChannel(c):
			continue
		case archived != c.Archived():
			continue
		}
		out.Channels = append(out.Channels, channelView(c, follows[c.ChannelID]))
	}
	if threadID := strings.TrimSpace(q.Get(wire.ChannelListThread)); forAgents && threadID != "" {
		// The turn's own thread (roomlog.AgentChannelList): listed for an
		// agent only when it is one the reader may read, as any channel.
		for _, c := range channels {
			if c.ChannelID == threadID && c.IsThread() && c.Admission().Admits() && access.MayReadChannel(c) {
				out.Channels = append(out.Channels, channelView(c, follows[c.ChannelID]))
			}
		}
	}
	out.Sections = s.knownSections(r.Context(), roomID, sections, access)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// knownSections projects the sections the reader may know (C11, C12): every
// open one; a members-only one for its members, the room's owners and
// admins (with its members: who reads it) and, by name alone, the room's
// people when its owners show it for joining.
func (s *Service) knownSections(ctx context.Context, roomID string, sections []store.RoomSection, access store.ChannelAccess) []wire.RoomSection {
	out := []wire.RoomSection{}
	for _, sec := range sections {
		if !access.MayKnowSection(sec) {
			continue
		}
		v := sectionView(sec)
		if sec.MembersOnly() {
			if access.MayKnowMembers(sec) {
				v.Members = s.sectionMemberIDs(ctx, roomID, sec.SectionID)
			}
			v.Joined = access.MaySeeSection(sec.SectionID)
		}
		out = append(out, v)
	}
	return out
}

// handleChannelDirectory lists every open channel of the room a member may
// join (not archived, never the people chat) with its purpose, owner and
// follower count, and the caller's own following (C18: the rail shows the
// joined ones, the directory all of them).
func (s *Service) handleChannelDirectory(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	member, ok := s.requireReadAs(w, r, roomID)
	if !ok {
		return
	}
	channels, err := s.Channels.ListChannels(r.Context(), roomID)
	var sections []store.RoomSection
	if err == nil {
		sections, err = s.Channels.ListSections(r.Context(), roomID)
	}
	var counts map[string]int
	if err == nil {
		counts, err = s.Channels.ChannelFollowerCounts(r.Context(), roomID)
	}
	var follows map[string]store.ChannelFollow
	if err == nil {
		user := ""
		if member {
			user = caller(r)
		}
		follows, err = s.followsOf(r.Context(), roomID, user)
	}
	if err != nil {
		obs.ServerError(w, "channel directory failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	access, ok := s.readerAccess(w, r, roomID)
	if !ok {
		return
	}
	out := wire.ChannelDirectory{Channels: []wire.ChannelDirectoryEntry{}, Sections: []wire.RoomSection{}}
	for _, c := range channels {
		if !c.Listed() || c.Archived() || !access.MayReadChannel(c) {
			continue
		}
		out.Channels = append(out.Channels, wire.ChannelDirectoryEntry{RoomChannel: channelView(c, follows[c.ChannelID]), Followers: counts[c.ChannelID]})
	}
	out.Sections = s.knownSections(r.Context(), roomID, sections, access)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleGetPeopleChat returns the room's people chat without creating it:
// 404 until a member first opens it. The room view reads it on entry (header
// badge, routing a deep link into the panel) without minting a channel for
// every room a member merely visits.
func (s *Service) handleGetPeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if _, ok := s.humanCaller(w, r, roomID); !ok {
		return
	}
	channels, err := s.Channels.ListChannels(r.Context(), roomID)
	if err != nil {
		obs.ServerError(w, "list channels failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, c := range channels {
		if c.IsPeople() {
			httpx.WriteJSON(w, http.StatusOK, channelView(c, store.ChannelFollow{}))
			return
		}
	}
	httpx.Error(w, "the room has no people chat yet", http.StatusNotFound)
}

// handlePeopleChat returns the room's people chat — a human-only side
// conversation agents never see — creating it on first open. Any member may
// open it; repeated and concurrent calls return the same channel.
func (s *Service) handlePeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if _, ok := s.humanCaller(w, r, roomID); !ok {
		return
	}
	ch, err := s.Channels.EnsurePeopleChannel(r.Context(), roomID, caller(r), s.Now())
	if err != nil {
		obs.ServerError(w, "open people chat failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, channelView(ch, store.ChannelFollow{}))
}

// handleCreateChannel adds a channel where the room's creation policy lets
// the caller put one (mayPlaceChannel): at the top level, or in a section.
// The creator is its owner.
func (s *Service) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	var body wire.ChannelCreateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	purpose, err := channelText(&body.Purpose, wire.MaxChannelPurpose, "purpose")
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Purpose = purpose
	room, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	sec, ok := s.targetSection(w, r, roomID, strings.TrimSpace(body.SectionID))
	if !ok {
		return
	}
	if !mayPlaceChannel(room, who.role, who.user, sec) {
		httpx.Error(w, "the room's channel creation policy does not let you create a channel there", http.StatusForbidden)
		return
	}
	ch, err := s.createChannel(r.Context(), roomID, who.user, body)
	if err != nil {
		obs.ServerError(w, "create channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.viewFor(w, r, roomID, ch.ChannelID, who.user)
}

// targetSection reads the section a channel goes to ("" is the top level,
// nil): a live, open section of the room, else 400.
func (s *Service) targetSection(w http.ResponseWriter, r *http.Request, roomID, sectionID string) (*store.RoomSection, bool) {
	if sectionID == "" {
		return nil, true
	}
	sec, ok, err := s.Channels.GetSection(r.Context(), roomID, sectionID)
	if err != nil {
		obs.ServerError(w, "read section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	access, okAccess := s.readerAccess(w, r, roomID)
	if !okAccess {
		return nil, false
	}
	if !ok || !access.MayKnowSection(sec) {
		httpx.Error(w, "no such section in this room", http.StatusBadRequest)
		return nil, false
	}
	// A channel goes into a members-only section only from inside it
	// (decision: channels PRD C11/C13: a channel moved in by someone who
	// cannot read it would take its content where they put it unseen).
	if !access.MaySeeSection(sec.SectionID) {
		httpx.Error(w, "join the section to put a channel in it", http.StatusForbidden)
		return nil, false
	}
	return &sec, true
}

// createChannel adds a non-default channel created and owned by creator
// (the creator may later change its setup). Every channel-adding path (the
// route, a handoff import) goes through it. The creator follows the channel
// they made (decision: pending owner; C18 names posting and mentions as
// joining, and a creator's rail without their own new channel would hide
// it from them).
func (s *Service) createChannel(ctx context.Context, roomID, creator string, req wire.ChannelCreateRequest) (store.RoomChannel, error) {
	ch := store.RoomChannel{
		ChannelID:   ids.New("ch_"),
		TenantID:    tenant.FromContext(ctx),
		RoomID:      roomID,
		Name:        strings.TrimSpace(req.Name),
		Purpose:     strings.TrimSpace(req.Purpose),
		SectionID:   strings.TrimSpace(req.SectionID),
		CreatedBy:   creator,
		OwnerID:     creator, // decision: channels PRD §2.1b (its creator by default)
		CreatedAtMs: s.Now(),
	}
	if err := s.Channels.PutChannel(ctx, ch); err != nil {
		return store.RoomChannel{}, err
	}
	if err := s.Channels.FollowChannel(ctx, roomID, ch.ChannelID, creator, ch.CreatedAtMs, store.FollowMarks{}); err != nil {
		log.Printf("room: room=%s channel=%s: the creator's follow failed: %v", roomID, ch.ChannelID, err)
	}
	s.publishChannelUpdated(ctx, roomID, ch.ChannelID, ch.SectionID)
	return ch, nil
}

// handlePatchChannel changes a channel's setup (mayManageChannel). The
// people chat is not configurable; the default channel keeps its name (it
// is renamed by no one) and never bars agents.
func (s *Service) handlePatchChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	ch, sec, ok := s.liveChannel(w, r, roomID)
	if !ok {
		return
	}
	if ch.IsPeople() {
		httpx.Error(w, "the people chat is not configurable", http.StatusConflict)
		return
	}
	if ch.IsThread() {
		s.patchThread(w, r, roomID, who, ch)
		return
	}
	if !mayManageChannel(who.role, who.user, ch, sec) {
		httpx.Error(w, "only the room's owner, an admin, the channel's owner or its section's owners change this channel", http.StatusForbidden)
		return
	}
	var body wire.ChannelPatchRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	patch, status, err := s.channelPatch(r, roomID, who, ch, body)
	if err != nil {
		httpx.WriteStatus(w, status, err.Error(), err)
		return
	}
	// Conditional: if a delete lands between the checks and this write,
	// UpdateChannel fails instead of resurrecting the row.
	if err := s.Channels.UpdateChannel(r.Context(), roomID, ch.ChannelID, patch); err != nil {
		if errors.Is(err, store.ErrChannelNotFound) {
			httpx.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "update channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sectionID := ch.SectionID
	if patch.SectionID != nil {
		sectionID = *patch.SectionID
	}
	s.publishChannelUpdated(r.Context(), roomID, ch.ChannelID, sectionID)
	s.viewFor(w, r, roomID, ch.ChannelID, who.user)
}

// channelPatch validates a patch request into a store patch; status is the
// refusal's (400, 403, 409, 500).
func (s *Service) channelPatch(r *http.Request, roomID string, who channelCaller, ch store.RoomChannel, body wire.ChannelPatchRequest) (store.ChannelPatch, int, error) {
	var p store.ChannelPatch
	if body.Name != nil {
		if ch.IsDefault {
			return p, http.StatusConflict, errBadRequest("the default channel cannot be renamed")
		}
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			return p, http.StatusBadRequest, errBadRequest("name is required")
		}
		p.Name = &name
	}
	for _, f := range []struct {
		in   *string
		out  **string
		max  int
		what string
	}{{body.Purpose, &p.Purpose, wire.MaxChannelPurpose, "purpose"}, {body.Rules, &p.Rules, wire.MaxChannelRules, "rules"}} {
		if f.in == nil {
			continue
		}
		t, err := channelText(f.in, f.max, f.what)
		if err != nil {
			return p, http.StatusBadRequest, err
		}
		*f.out = &t
	}
	if body.SectionID != nil {
		target := strings.TrimSpace(*body.SectionID)
		if target != ch.SectionID {
			sec, err := s.sectionOf(r.Context(), roomID, target)
			if err != nil {
				return p, http.StatusInternalServerError, err
			}
			if target != "" && sec == nil {
				return p, http.StatusBadRequest, errBadRequest("no such section in this room")
			}
			if sec != nil && sec.MembersOnly() {
				access, err := s.memberAccess(r.Context(), roomID, who)
				if err != nil {
					return p, http.StatusInternalServerError, err
				}
				if !access.MaySeeSection(sec.SectionID) {
					return p, http.StatusForbidden, errBadRequest("join the section to put a channel in it")
				}
			}
			room, err := s.Store.GetRoom(r.Context(), roomID)
			if err != nil {
				return p, http.StatusNotFound, err
			}
			if !mayPlaceChannel(room, who.role, who.user, sec) {
				return p, http.StatusForbidden, errBadRequest("the room's channel creation policy does not let you move a channel there")
			}
		}
		p.SectionID = &target
	}
	p.Position = body.Position
	if body.OwnerID != nil {
		owner := strings.TrimSpace(*body.OwnerID)
		// decision: pending owner (a channel's owner answers for its setup,
		// §2.1b; a visitor is read-only, so only a member who may chat owns
		// one).
		m, ok, err := s.Store.GetMember(r.Context(), roomID, owner)
		if err != nil {
			return p, http.StatusInternalServerError, err
		}
		if !ok || !m.Human() || m.Role == store.RoleVisitor {
			return p, http.StatusBadRequest, errBadRequest("a channel's owner is a member of the room who may post")
		}
		p.OwnerID = &owner
	}
	if body.Settings != nil {
		settings, err := cleanChannelSettings(*body.Settings)
		if err != nil {
			return p, http.StatusBadRequest, err
		}
		// decision: pending owner. The default channel never bars agents:
		// it is where a request goes when it names no channel, and an
		// agent's degraded read of the room falls back to it
		// (roomlog.AgentChannelList).
		if ch.IsDefault && settings.NoAgents {
			return p, http.StatusConflict, errBadRequest("the default channel always admits agents")
		}
		p.Settings = &settings
	}
	return p, 0, nil
}

// handleArchiveChannel archives (archive true) or restores a channel
// (mayManageChannel). The default channel and the people chat are never
// archived. decision: channels PRD C10.
func (s *Service) handleArchiveChannel(archive bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("roomId")
		who, ok := s.humanCaller(w, r, roomID)
		if !ok {
			return
		}
		ch, sec, ok := s.liveChannel(w, r, roomID)
		if !ok {
			return
		}
		if ch.IsDefault || ch.IsPeople() {
			httpx.Error(w, "the default channel and the people chat cannot be archived", http.StatusConflict)
			return
		}
		if ch.IsThread() {
			// decision: channels PRD C3 (a person archives a thread: any
			// member who may post in it, as anyone who may post edits its
			// title, §2.2).
			if who.role == store.RoleVisitor {
				httpx.Error(w, "visitors do not archive threads", http.StatusForbidden)
				return
			}
		} else if !mayManageChannel(who.role, who.user, ch, sec) {
			httpx.Error(w, "only the room's owner, an admin, the channel's owner or its section's owners archive or restore this channel", http.StatusForbidden)
			return
		}
		var at *int64
		if archive {
			now := s.Now()
			at = &now
		}
		if err := s.Channels.SetChannelArchived(r.Context(), roomID, ch.ChannelID, at); err != nil {
			if errors.Is(err, store.ErrChannelNotFound) {
				httpx.Error(w, "channel not found", http.StatusNotFound)
				return
			}
			obs.ServerError(w, "archive channel failed", err)
			httpx.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishChannelUpdated(r.Context(), roomID, ch.ChannelID, ch.SectionID)
		s.viewFor(w, r, roomID, ch.ChannelID, who.user)
	}
}

// handleDeleteChannel soft-deletes a channel (owner/admin only); the default
// channel cannot be deleted — every room must keep a message stream.
func (s *Service) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	role, ok, err := s.Store.MemberRole(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || !role.CanInvite() {
		httpx.Error(w, "only the owner or an admin may delete channels", http.StatusForbidden)
		return
	}
	channelID := r.PathValue("channelId")
	ch, ok, err := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if err != nil {
		obs.ServerError(w, "read channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if ch.IsDefault || ch.IsPeople() {
		httpx.Error(w, "the default channel and the people chat cannot be deleted", http.StatusConflict)
		return
	}
	if err := s.Channels.DeleteChannel(r.Context(), roomID, channelID, s.Now()); err != nil {
		obs.ServerError(w, "delete channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishChannelUpdated(r.Context(), roomID, channelID, ch.SectionID)
	w.WriteHeader(http.StatusOK)
}

// handleFollowChannel makes the caller follow (join) a channel and, when the
// body names them, star or hide it for themselves or put it in one of their
// groups (C24); follow false unfollows, which takes it out of its group.
// Following is the caller's own order, never access: any human member of
// the room follows, a visitor included (decision: pending owner; §2.1b
// "personal order ... changes nothing for others"). The people chat is
// never followed (it is reached through its own panel).
func (s *Service) handleFollowChannel(follow bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("roomId")
		who, ok := s.humanCaller(w, r, roomID)
		if !ok {
			return
		}
		ch, _, ok := s.liveChannel(w, r, roomID)
		if !ok {
			return
		}
		if ch.IsPeople() {
			httpx.Error(w, "the people chat is not followed", http.StatusConflict)
			return
		}
		if !follow && ch.IsDefault {
			httpx.Error(w, "every member is in the room's default channel: hide it from your list instead", http.StatusConflict)
			return
		}
		var err error
		if follow {
			var body wire.ChannelFollowRequest
			if !httpx.DecodeOptionalJSON(w, r, maxJSONBodyBytes, &body) {
				return
			}
			if body.Level != nil && !wire.IsNotifyLevel(*body.Level) {
				httpx.Error(w, "level is one of all, mentions, muted, or empty for the default", http.StatusBadRequest)
				return
			}
			if body.GroupID != nil && *body.GroupID != "" {
				// A channel goes only into one of the caller's own groups.
				if found, gerr := s.ownGroup(r.Context(), roomID, who.user, *body.GroupID); gerr != nil {
					err = gerr
				} else if !found {
					httpx.Error(w, "not one of your groups", http.StatusBadRequest)
					return
				}
			}
			if err == nil {
				err = s.Channels.FollowChannel(r.Context(), roomID, ch.ChannelID, who.user, s.Now(), store.FollowMarks{Starred: body.Starred, Hidden: body.Hidden, Group: body.GroupID, Level: body.Level})
			}
		} else {
			err = s.Channels.UnfollowChannel(r.Context(), roomID, ch.ChannelID, who.user)
		}
		if err != nil {
			obs.ServerError(w, "follow channel failed", err)
			httpx.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.Publisher != nil {
			s.Publisher.Publish(r.Context(), wire.Event{RoomID: roomID, ChannelID: ch.ChannelID, Kind: wire.EventChannelFollow,
				Payload: wire.ChannelFollowPayload{ChannelID: ch.ChannelID, UserID: who.user}, AtUnixMs: s.Now()})
		}
		s.viewFor(w, r, roomID, ch.ChannelID, who.user)
	}
}

// joinRecommended makes a new member follow the room's recommended
// channels (C18: a new member joins them). Best effort: a failed read or
// write is logged and never fails the join, which already happened.
func (s *Service) joinRecommended(ctx context.Context, roomID, userID string) {
	if s.Channels == nil {
		return
	}
	room, err := s.Store.GetRoom(ctx, roomID)
	if err != nil {
		log.Printf("room: room=%s: recommended channels for %s not joined: %v", roomID, userID, err)
		return
	}
	for _, id := range room.RecommendedChannels {
		// decision: channels PRD C18 (a new member joins the recommended
		// channels). A recommended channel deleted or archived since is
		// skipped: there is nothing live to join.
		ch, ok, err := s.Channels.GetChannel(ctx, roomID, id)
		if err != nil {
			log.Printf("room: room=%s channel=%s: recommended channel read failed for %s: %v", roomID, id, userID, err)
			continue
		}
		if !ok || ch.Archived() || ch.IsPeople() {
			continue
		}
		if err := s.Channels.FollowChannel(ctx, roomID, id, userID, s.Now(), store.FollowMarks{}); err != nil {
			log.Printf("room: room=%s channel=%s: joining a recommended channel failed for %s: %v", roomID, id, userID, err)
		}
	}
}
