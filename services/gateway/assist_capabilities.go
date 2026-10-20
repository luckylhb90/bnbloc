package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The capabilities context and the turn of the room context
// (docs/prd/2026-10-20-never-a-dead-end.md §2.2): what the member could
// use and does not here, and the request and reply a "find a way" assist
// reads. Each source is the one its own surface reads: the member catalog's
// rule (memberComponents.listed), realtime's roster, the room service's
// members, shareagent's platform agents, realtime's history.

// assistCapabilities is the capabilities context as claims' member sees
// it: the executable components the member catalog lists to them, the
// organization's platform agents and, with a room (rc, roomID; "" for
// none), its agents and how many other people it has. hasOwnAgent is the
// client's word. Names and one-liners only.
func (s *Service) assistCapabilities(ctx context.Context, claims, rc auth.Claims, roomID string, hasOwnAgent bool) (*assist.Capabilities, error) {
	out := &assist.Capabilities{OwnAgent: hasOwnAgent, InRoom: roomID != ""}
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return nil, err
	}
	for _, rec := range view.list {
		vis, listed := view.listed(rec)
		if !listed {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			return nil, err
		}
		out.Components = append(out.Components, assist.Capability{Name: rec.Name, Kind: rec.Kind, Description: cfg.Description, Installed: vis.Installed})
	}
	sort.SliceStable(out.Components, func(i, j int) bool { return out.Components[i].Name < out.Components[j].Name })
	if out.PlatformAgents, err = s.assistPlatformAgents(ctx, claims); err != nil {
		return nil, err
	}
	if roomID == "" {
		return out, nil
	}
	roster, err := s.roomRoster(ctx, rc, roomID)
	if err != nil {
		return nil, err
	}
	for _, a := range roster {
		out.RoomAgents = append(out.RoomAgents, rosterHandle(a))
	}
	sort.Strings(out.RoomAgents)
	var members wire.RoomMembersReply
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/members", "", nil, rc.Subject, rc.Tenant, &members); err != nil {
		return nil, err
	}
	for _, m := range members.Members {
		if !m.AgentHost && m.UserID != rc.Subject {
			out.OtherMembers++
		}
	}
	return out, nil
}

// rosterHandle is a roster agent's handle as the web names it
// (web/src/lib/roomAgents.ts agentHandle): "core/owner/provider" with a
// core, else "owner/provider" (roomlog.AgentSpeaker).
func rosterHandle(a wire.RosterAgent) string {
	if a.Core != "" {
		return a.Core + "/" + a.OwnerUserID + "/" + a.Provider
	}
	return roomlog.AgentSpeaker(a.OwnerUserID, a.Provider)
}

// assistPlatformAgents names the organization's platform agents
// ("core/provider") as shareagent lists them to the member. Platform
// agents are optional: a deployment without them (shareagent unset, or
// answering it has none configured) lists none.
func (s *Service) assistPlatformAgents(ctx context.Context, claims auth.Claims) ([]string, error) {
	base := strings.TrimRight(s.Backends.ShareAgent, "/")
	if base == "" {
		return nil, nil
	}
	resp, err := callBackend(ctx, http.MethodGet, base+"/v1/platform-agents", nil, claims.Subject, claims.Tenant)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Info("bnbloc gateway: platform agents not listed for an assist", "status", resp.StatusCode)
		return nil, nil
	}
	var reply wire.PlatformAgentsReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return nil, fmt.Errorf("decode platform agents: %w", err)
	}
	var out []string
	for _, a := range reply.Agents {
		out = append(out, a.Core+"/"+a.Provider)
	}
	return out, nil
}

// roomRoster is the room's agents online now, as realtime's roster lists
// them to the member: the one read of it for the gateway (an assist's
// capabilities, a schedule run's list_agents).
func (s *Service) roomRoster(ctx context.Context, claims auth.Claims, roomID string) ([]wire.RosterAgent, error) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		return nil, nil
	}
	resp, err := callBackend(ctx, http.MethodGet, base+"/v1/rooms/"+url.PathEscape(roomID)+"/agents", nil, claims.Subject, claims.Tenant)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, upstreamStatusError(resp)
	}
	var roster struct {
		Agents []wire.RosterAgent `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&roster); err != nil {
		return nil, fmt.Errorf("decode the room's agents: %w", err)
	}
	return roster.Agents, nil
}

// assistTurn reads the turn of an agent reply as the member: the reply's
// row (replySeq, in the channel) and the request it answers (the row its
// payload's sourceSeq names), through the one history row decoder. A
// sequence that is not an agent's reply there is errAssistNotFound; a
// request no longer there leaves the turn's request empty.
func (s *Service) assistTurn(ctx context.Context, claims auth.Claims, roomID, channelID string, replySeq int64) (*assist.Turn, error) {
	reply, err := s.assistRow(ctx, claims, roomID, channelID, replySeq)
	if err != nil {
		return nil, err
	}
	var turn assist.Turn
	var source int64
	for _, e := range reply {
		if e.Kind != roomlog.KindAgent {
			return nil, errAssistNotFound
		}
		turn.Reply.Speaker, source = e.Speaker, max(source, e.SourceSeq)
		turn.Reply.Text = strings.TrimSpace(turn.Reply.Text + "\n\n" + e.Text)
	}
	if turn.Reply.Text == "" {
		return nil, errAssistNotFound
	}
	if source <= 0 {
		return &turn, nil
	}
	request, err := s.assistRow(ctx, claims, roomID, channelID, source)
	switch {
	case errors.Is(err, errAssistNotFound):
		return &turn, nil
	case err != nil:
		return nil, err
	}
	for _, e := range request {
		if e.Kind == roomlog.KindHuman {
			turn.Request = assist.Line{Speaker: e.Speaker, Text: e.Text}
			if e.To != "" {
				turn.Request.Speaker += " to " + e.To
			}
		}
	}
	return &turn, nil
}

// assistRow is the entries of the one row at seq in the channel
// (errAssistNotFound when there is none, or it holds no text).
func (s *Service) assistRow(ctx context.Context, claims auth.Claims, roomID, channelID string, seq int64) ([]roomlog.Entry, error) {
	rows, err := s.roomHistory(ctx, memberReader(claims), roomID, channelID, seq+1, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].Sequence != seq {
		return nil, errAssistNotFound
	}
	entries := roomlog.DecodeRow(rows[0])
	if len(entries) == 0 {
		return nil, errAssistNotFound
	}
	return entries, nil
}
