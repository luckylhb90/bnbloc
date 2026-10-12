package roomlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Agent-facing room history: the one reader every path that feeds the room
// back to an agent goes through — the bridge's .room/ materialization and
// pushed context, the room tools' read_room and ask_agent's reply poller.
//
// Scope (decision O1, docs/prd/2026-10-12-code-health-remediation.md §5):
// an agent reads exactly the channels realtime's agentMayReadChannel admits
// for an agent reader — every channel of the room except the people chat.
// The bridge reads with its owner's (human) session, so realtime does not
// apply that guard to it; AgentChannels does: it reads the room's channel
// listing, which never lists the people chat, and History never reads a
// channel outside it.
//
// Decoding: DecodeRow is the one decoder of a history row. Notices (a
// refused or failed drive, a team lead's progress: wire.JobNotice) are
// skipped — an agent must never read a notice as something it or another
// agent said.

// Fetch retrieves one gateway path (the bridge supplies its authenticated
// gateway GET), so this package stays transport-neutral.
type Fetch func(ctx context.Context, path string) ([]byte, error)

// Row is one room history row as the gateway serves it (GET
// /v1/rooms/{id}/history, realtime handleHistory).
type Row struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
	AtUnixMs int64           `json:"atUnixMs"`
}

const (
	// fetchPageRows is the default history page size per request.
	fetchPageRows = 200
	// maxFetchRows bounds one read across all channels by default.
	maxFetchRows = 2400
)

// HistoryQuery narrows one History read.
type HistoryQuery struct {
	// Only reads one channel, which must be one an agent may read ("" =
	// every agent-readable channel).
	Only string
	// After is the delta boundary: a channel stops paging once its pages
	// cross it. Entries at or below it may still be returned (the pages that
	// cross the boundary); callers that want only the delta filter by Seq.
	After int64
	// MaxRows bounds the rows read across all channels (0 = maxFetchRows).
	MaxRows int
	// PageRows is the page size (0 = fetchPageRows).
	PageRows int
	// Pages bounds the pages read per channel (0 = until the boundary or
	// the channel's start).
	Pages int
}

// ErrChannelNotReadable: HistoryQuery.Only names a channel no agent may read
// (the people chat, or one that is not the room's).
var ErrChannelNotReadable = errors.New("an agent may not read that channel")

// AgentChannels lists the channels of a room an agent may read: the room's
// channel listing (GET /v1/rooms/{id}/channels), which never lists the people
// chat. A listing that fails or is empty yields the default channel alone
// (the empty id, which realtime resolves to the room's default channel —
// never the people chat), so a degraded listing narrows the read instead of
// failing the turn; ok reports whether the listing was read.
func AgentChannels(ctx context.Context, fetch Fetch, room string) (channels []string, ok bool) {
	raw, err := fetch(ctx, "/v1/rooms/"+url.PathEscape(room)+"/channels")
	if err != nil {
		slog.Debug("bnbloc roomlog channel listing failed; reading the default channel",
			"event", "bnbloc.roomlog.channels_failed", "room", room, "error", err)
		return []string{""}, false
	}
	var parsed struct {
		Channels []struct {
			ChannelID string `json:"channelId"`
		} `json:"channels"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return []string{""}, false
	}
	for _, c := range parsed.Channels {
		if id := strings.TrimSpace(c.ChannelID); id != "" {
			channels = append(channels, id)
		}
	}
	if len(channels) == 0 {
		return []string{""}, true
	}
	return channels, true
}

// History reads the room's history across the channels an agent may read
// (AgentChannels, narrowed by q.Only) and returns the decoded entries in
// chronological order.
//
// The history API's contract: one request returns the newest `limit`
// messages with seq < before (or the newest overall when before is unset),
// in ASCENDING order — page[0] is the page's oldest. Paging walks backward
// from each page's oldest seq. A channel is done when a short or empty page
// arrives, its pages cross q.After, or q.Pages pages were read. The read
// stops globally at q.MaxRows rows; complete reports whether every channel
// was fully covered (false: the cap truncated the read, keeping the newest).
func History(ctx context.Context, fetch Fetch, room string, q HistoryQuery) (entries []Entry, complete bool, err error) {
	channels, listed := AgentChannels(ctx, fetch, room)
	if q.Only != "" {
		if !listed || !slices.Contains(channels, q.Only) {
			return nil, false, ErrChannelNotReadable
		}
		channels = []string{q.Only}
	}
	maxRows := q.MaxRows
	if maxRows <= 0 {
		maxRows = maxFetchRows
	}
	pageRows := q.PageRows
	if pageRows <= 0 {
		pageRows = fetchPageRows
	}
	var rows []Row
	truncated := false
	for _, ch := range channels {
		var before int64
		for pages := 0; q.Pages <= 0 || pages < q.Pages; pages++ {
			if len(rows) >= maxRows {
				truncated = true
				break
			}
			v := url.Values{"limit": {strconv.Itoa(pageRows)}}
			if ch != "" {
				v.Set("channelId", ch)
			}
			if before > 0 {
				v.Set("before", strconv.FormatInt(before, 10))
			}
			raw, err := fetch(ctx, fmt.Sprintf("/v1/rooms/%s/history?%s", url.PathEscape(room), v.Encode()))
			if err != nil {
				return nil, false, err
			}
			var page struct {
				Messages []Row `json:"messages"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, false, fmt.Errorf("decode room history: %w", err)
			}
			rows = append(rows, page.Messages...)
			if len(page.Messages) < pageRows || page.Messages[0].Sequence <= q.After {
				break // channel exhausted, or the delta boundary crossed
			}
			before = page.Messages[0].Sequence
		}
	}
	// Newest first, de-duplicated (a row is one sequence), capped, then
	// decoded in chronological order.
	sort.Slice(rows, func(i, j int) bool { return rows[i].Sequence > rows[j].Sequence })
	rows = slices.CompactFunc(rows, func(a, b Row) bool { return a.Sequence == b.Sequence })
	if len(rows) > maxRows {
		rows, truncated = rows[:maxRows], true
	}
	for i := len(rows) - 1; i >= 0; i-- {
		entries = append(entries, DecodeRow(rows[i])...)
	}
	return entries, !truncated, nil
}

// DecodeRow maps one history row to the entries an agent reads: a
// chat_message is one person's line; an agent_messages row carries one entry
// per reply, attributed to its agent (owner/provider). Blank lines and
// notices yield nothing.
func DecodeRow(r Row) []Entry {
	switch r.Kind {
	case wire.KindChatMessage:
		var p struct {
			UserID    string `json:"userId"`
			Text      string `json:"text"`
			Recipient *struct {
				OwnerID  string `json:"ownerId"`
				Provider string `json:"provider"`
			} `json:"recipient"`
			DispatchedBy *wire.DispatchMarker `json:"dispatchedBy"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || strings.TrimSpace(p.Text) == "" {
			return nil
		}
		e := Entry{Seq: r.Sequence, AtMs: r.AtUnixMs, Speaker: p.UserID, Kind: KindHuman, Text: p.Text}
		if p.Recipient != nil && p.Recipient.Provider != "" {
			e.To = AgentSpeaker(p.Recipient.OwnerID, p.Recipient.Provider)
		}
		if p.DispatchedBy != nil {
			e.Via = p.DispatchedBy.Via
		}
		return []Entry{e}
	case wire.KindAgentMessages:
		var p struct {
			AgentSessionID string `json:"agentSessionId"`
			Messages       []struct {
				Payload struct {
					Role        string `json:"role"`
					Text        string `json:"text"`
					Notice      string `json:"notice"`
					OwnerUserID string `json:"ownerUserId"`
					Provider    string `json:"provider"`
				} `json:"payload"`
			} `json:"messages"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || wire.IsJobNoticeSession(p.AgentSessionID) {
			return nil
		}
		out := make([]Entry, 0, len(p.Messages))
		for _, m := range p.Messages {
			mp := m.Payload
			if mp.Notice != "" || strings.TrimSpace(mp.Text) == "" {
				continue
			}
			out = append(out, Entry{
				Seq: r.Sequence, AtMs: r.AtUnixMs,
				Speaker: AgentSpeaker(mp.OwnerUserID, mp.Provider), Kind: KindAgent, Text: mp.Text,
				Owner: mp.OwnerUserID, Provider: mp.Provider, Session: p.AgentSessionID, Role: mp.Role,
			})
		}
		return out
	}
	return nil
}
