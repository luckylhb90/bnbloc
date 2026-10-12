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

// roomExpert resolves the room's expert for a job's turn (nil when the room
// has none, or it is the chat's own expert). It must still be usable in the
// room: a room expert whose pin was removed is reported missing.
func roomExpert(ctx context.Context, st store.ContentComponentStore, requester extpipe.Viewer, job store.AgentJob) (store.ContentComponent, store.ContentComponentVersion, string, error) {
	uses, err := st.ListContentUses(ctx, store.ContentUseRoomExpert, job.RoomID)
	if err != nil || len(uses) == 0 {
		return store.ContentComponent{}, store.ContentComponentVersion{}, "", err
	}
	id := uses[0].ComponentID
	if id == job.ExpertID {
		return store.ContentComponent{}, store.ContentComponentVersion{}, "", nil
	}
	c, v, err := Usable(ctx, st, requester, job.RoomID, id, "")
	if errors.Is(err, ErrNotUsable) {
		name := id
		if gone, gerr := st.GetContentComponent(ctx, id); gerr == nil {
			name = firstNonEmpty(gone.DisplayName, gone.Name)
		}
		return store.ContentComponent{}, store.ContentComponentVersion{}, "room expert " + name, nil
	}
	return c, v, "", err
}
