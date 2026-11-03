package contentuse

import (
	"context"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// TeamRoster is a team's roster as its manifest names it (no resolution):
// the Library card's view.
func TeamRoster(t *contentpkg.TeamSpec) *wire.ContentTeam {
	if t == nil {
		return nil
	}
	out := &wire.ContentTeam{Lead: t.LeadRole(), Members: make([]wire.ContentTeamMember, 0, len(t.Members))}
	for _, m := range t.Members {
		out.Members = append(out.Members, wire.ContentTeamMember{Role: m.Role, Expert: m.Expert, DefaultAgent: m.DefaultAgent, Description: m.Description})
	}
	return out
}

// ResolveTeam resolves each role's expert as the requester, by the rule
// skill requirements follow: the highest version satisfying "name@range"
// among the experts they may use and the experts pinned in the room. A
// member expert that is itself a team is refused (teams do not nest).
func ResolveTeam(ctx context.Context, st store.ContentComponentStore, requester extpipe.Viewer, roomID string, t *contentpkg.TeamSpec) (*wire.ContentTeam, error) {
	out := TeamRoster(t)
	if out == nil {
		return nil, nil
	}
	roomUses, err := st.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return nil, err
	}
	for i, m := range out.Members {
		c, v, ok, err := resolveReference(ctx, st, requester, requester.Tenant, roomUses, m.Expert, contentpkg.KindExpert)
		if err != nil {
			return nil, err
		}
		if !ok {
			out.Members[i].Missing = "no expert " + m.Expert + " that " + requester.Subject + " may use here"
			continue
		}
		man, err := Manifest(v)
		if err != nil {
			return nil, err
		}
		if man.Team != nil {
			out.Members[i].Missing = "the expert " + m.Expert + " is itself a team (teams do not nest)"
			continue
		}
		out.Members[i].Resolved = &wire.ContentTeamExpert{
			ComponentID: c.ID, Name: c.Name, DisplayName: man.DisplayName, Description: man.Description, Version: v.Version,
		}
	}
	return out, nil
}

// ChannelExpertID is the expert a channel names for its turns (channels PRD
// C9): "" for none. The one read of a channel's expert; a thread's turns
// wear its parent's (the caller passes the parent, the gateway's
// effectiveContent).
func ChannelExpertID(ctx context.Context, st store.ContentComponentStore, roomID, channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	uses, err := st.ListContentUses(ctx, store.ContentUseChannelExpert, store.ChannelExpertScopeID(roomID, channelID))
	if err != nil || len(uses) == 0 {
		return "", err
	}
	return uses[0].ComponentID, nil
}

// roomExpert resolves the expert a job's turn wears under the chat's own:
// its channel's (C9), else the room's (nil when there is none, or it is the
// chat's own expert). It must still be usable in the room: one whose pin was
// removed is reported missing.
func roomExpert(ctx context.Context, st store.ContentComponentStore, requester extpipe.Viewer, job store.AgentJob) (store.ContentComponent, store.ContentComponentVersion, string, bool, error) {
	id, err := ChannelExpertID(ctx, st, job.RoomID, job.ChannelID)
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, "", false, err
	}
	channel := id != ""
	if id == "" {
		uses, err := st.ListContentUses(ctx, store.ContentUseRoomExpert, job.RoomID)
		if err != nil || len(uses) == 0 {
			return store.ContentComponent{}, store.ContentComponentVersion{}, "", false, err
		}
		id = uses[0].ComponentID
	}
	if id == job.ExpertID {
		return store.ContentComponent{}, store.ContentComponentVersion{}, "", false, nil
	}
	c, v, err := Usable(ctx, st, requester, job.RoomID, id, "")
	if errors.Is(err, ErrNotUsable) {
		name := id
		if gone, gerr := st.GetContentComponent(ctx, id); gerr == nil {
			name = firstNonEmpty(gone.DisplayName, gone.Name)
		}
		return store.ContentComponent{}, store.ContentComponentVersion{}, "room expert " + name, false, nil
	}
	return c, v, "", channel, err
}
