package gateway

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// The conversation context (wire.AssistContextConversation,
// docs/prd/2026-10-20-complete-room-capture.md §2.1): a room's latest
// messages across its channels, as its member reads them, for the
// room-saving assist (describe-room). The channels are the room service's
// listing (never the people chat), each read through the gateway's one
// history read (roomHistory) and decoded by the one row decoder
// (roomlog.DecodeRow); assist.NewConversation keeps the newest within its
// budget.

// maxConversationChannels bounds the channels a conversation reads, in
// the order the room lists them (the default channel first).
const maxConversationChannels = 12

// assistConversation reads roomID's conversation as the member in claims:
// each listed channel's latest assist.MaxConversationLines rows, merged by
// their room sequence, newest first, into the budget of
// assist.NewConversation.
func (s *Service) assistConversation(ctx context.Context, claims auth.Claims, roomID string) (*assist.Conversation, error) {
	name, err := s.assistRoomName(ctx, claims, roomID)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Channels []struct {
			ChannelID string `json:"channelId"`
			Name      string `json:"name"`
			IsDefault bool   `json:"isDefault"`
		} `json:"channels"`
	}
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/channels", "", nil, claims.Subject, claims.Tenant, &listing); err != nil {
		return nil, err
	}
	// The default channel ("": the history's default), then the others.
	read := []string{""}
	names := map[string]string{} // channel id -> name; the default's ""
	for _, ch := range listing.Channels {
		if !ch.IsDefault && len(read) < maxConversationChannels {
			names[ch.ChannelID] = ch.Name
			read = append(read, ch.ChannelID)
		}
	}
	var entries []roomlog.Entry
	for _, channelID := range read {
		rows, err := s.roomHistory(ctx, memberReader(claims), roomID, channelID, 0, assist.MaxConversationLines)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			for _, e := range roomlog.DecodeRow(row) {
				if strings.TrimSpace(e.Text) != "" {
					entries = append(entries, e)
				}
			}
		}
	}
	// Newest first: a row's entries keep their order, reversed with it.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	lines := make([]assist.Line, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		lines = append(lines, assist.Line{Speaker: e.Speaker, Text: e.Text, Channel: names[e.ChannelID]})
	}
	return assist.NewConversation(name, lines), nil
}
