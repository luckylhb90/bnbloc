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
	Sequence  int64           `json:"sequence"`
	ChannelID string          `json:"channelId"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	AtUnixMs  int64           `json:"atUnixMs"`
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
	// Before starts every channel's read below this sequence (0 = from the
	// newest): a range read (After..Before) of records being rewritten.
	Before int64
	// MaxRows bounds the rows read across all channels (0 = maxFetchRows),
	// shared fairly: every channel gets its share before any gets more, so
	// a busy channel never starves the others.
	MaxRows int
	// PageRows is the page size (0 = fetchPageRows).
	PageRows int
	// Pages bounds the pages read per channel (0 = until the boundary or
	// the channel's start).
	Pages int
	// Job is the turn's job, whose requester's readable channels the read
	// covers (AgentChannelList); "" reads as the bridge's owner.
	Job string
}

// ErrChannelNotReadable: HistoryQuery.Only names a channel no agent may read
// (the people chat, or one that is not the room's).
var ErrChannelNotReadable = errors.New("an agent may not read that channel")

// Channel is one channel an agent may read: its id and its name.
type Channel struct {
	ID   string
	Name string
}

// AgentChannelsQuery is the query of the room service's channel listing for
// agents (services/room handleListChannels reads wire.ChannelListForAgents).
const AgentChannelsQuery = wire.ChannelListForAgents + "=1"

// AgentChannels lists the ids of the channels of a room an agent may read
// (AgentChannelList).
func AgentChannels(ctx context.Context, fetch Fetch, room string) (channels []string, ok bool) {
	list, ok := AgentChannelList(ctx, fetch, room, "")
	for _, c := range list {
		channels = append(channels, c.ID)
	}
	return channels, ok
}

// AgentChannelList lists the channels of a room an agent may read: the
// room's channel listing for agents (GET /v1/rooms/{id}/channels?agents=1),
// which never lists the people chat nor a channel whose settings bar agents
// (store.ChannelAdmitsAgents) and keeps archived channels, whose history
// stays readable; the default channel first. A listing that fails or is
// empty yields the default channel alone (the empty id, which realtime
// resolves to the room's default channel — never the people chat, and the
// default channel never bars agents), so a degraded listing narrows the read
// instead of failing the turn; ok reports whether the listing was read and
// named a channel.
//
// job is the turn's job ("" none): the listing is read with its grant
// (?job=), so the gateway reads it as the job's requester and it names only
// the channels that person may read (store.MayReadChannel, C14: an agent
// reads as the person it works for, never as the bridge's owner; a
// borrowed agent working for someone outside a members-only section sees
// none of it). Without a job (no turn) the listing is the owner's.
func AgentChannelList(ctx context.Context, fetch Fetch, room, job string) (channels []Channel, ok bool) {
	q := AgentChannelsQuery
	if job != "" {
		q += "&job=" + url.QueryEscape(job)
	}
	raw, err := fetch(ctx, "/v1/rooms/"+url.PathEscape(room)+"/channels?"+q)
	if err != nil {
		slog.Debug("bnbloc roomlog channel listing failed; reading the default channel",
			"event", "bnbloc.roomlog.channels_failed", "room", room, "error", err)
		return []Channel{{}}, false
	}
	var parsed struct {
		Channels []struct {
			ChannelID string `json:"channelId"`
			Name      string `json:"name"`
		} `json:"channels"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return []Channel{{}}, false
	}
	for _, c := range parsed.Channels {
		if id := strings.TrimSpace(c.ChannelID); id != "" {
			channels = append(channels, Channel{ID: id, Name: strings.TrimSpace(c.Name)})
		}
	}
	if len(channels) == 0 { // a room always has its default channel
		return []Channel{{}}, false
	}
	return channels, true
}

// ChannelOf resolves a channel id as a job names it ("" = the room's default
// channel, which the listing names first) to the listed channel; ok is false
// when the room does not list it.
func ChannelOf(list []Channel, id string) (Channel, bool) {
	if id == "" && len(list) > 0 {
		return list[0], true
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
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
	channels, listed := AgentChannelList(ctx, fetch, room, q.Job)
	if q.Only != "" {
		only, found := ChannelOf(channels, q.Only)
		if !listed || !found {
			return nil, false, ErrChannelNotReadable
		}
		channels = []Channel{only}
	}
	maxRows := q.MaxRows
	if maxRows <= 0 {
		maxRows = maxFetchRows
	}
	pageRows := q.PageRows
	if pageRows <= 0 {
		pageRows = fetchPageRows
	}
	// Each channel's read: where its next page starts, whether it is done.
	type cursor struct {
		ch     Channel
		before int64
		pages  int
		done   bool
		rows   []Row
	}
	cursors := make([]*cursor, len(channels))
	for i, ch := range channels {
		cursors[i] = &cursor{ch: ch, before: q.Before}
	}
	total := 0
	// read pages one channel until it is done or holds quota rows.
	read := func(c *cursor, quota int) error {
		for !c.done && len(c.rows) < quota && total < maxRows {
			if q.Pages > 0 && c.pages >= q.Pages {
				c.done = true
				break
			}
			limit := min(pageRows, quota-len(c.rows), maxRows-total)
			v := url.Values{"limit": {strconv.Itoa(limit)}}
			if c.ch.ID != "" {
				v.Set("channelId", c.ch.ID)
			}
			if c.before > 0 {
				v.Set("before", strconv.FormatInt(c.before, 10))
			}
			raw, err := fetch(ctx, fmt.Sprintf("/v1/rooms/%s/history?%s", url.PathEscape(room), v.Encode()))
			if err != nil {
				return err
			}
			var page struct {
				Messages []Row `json:"messages"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return fmt.Errorf("decode room history: %w", err)
			}
			c.pages++
			for i := range page.Messages {
				if page.Messages[i].ChannelID == "" {
					page.Messages[i].ChannelID = c.ch.ID
				}
			}
			c.rows = append(c.rows, page.Messages...)
			total += len(page.Messages)
			if len(page.Messages) < limit || len(page.Messages) == 0 || page.Messages[0].Sequence <= q.After {
				c.done = true // channel exhausted, or the delta boundary crossed
				break
			}
			c.before = page.Messages[0].Sequence
		}
		return nil
	}
	// Every channel gets its share first; what the done ones leave is
	// shared among the rest, until the cap or every channel is done.
	for {
		open := 0
		for _, c := range cursors {
			if !c.done {
				open++
			}
		}
		if open == 0 || total >= maxRows {
			break
		}
		share := max((maxRows-total)/open, 1)
		for _, c := range cursors {
			if c.done {
				continue
			}
			if err := read(c, len(c.rows)+share); err != nil {
				return nil, false, err
			}
		}
	}
	complete = true
	var rows []Row
	for _, c := range cursors {
		if !c.done {
			complete = false
		}
		rows = append(rows, c.rows...)
	}
	// Newest first, de-duplicated (a row is one sequence), capped, then
	// decoded in chronological order.
	sort.Slice(rows, func(i, j int) bool { return rows[i].Sequence > rows[j].Sequence })
	rows = slices.CompactFunc(rows, func(a, b Row) bool { return a.Sequence == b.Sequence })
	if len(rows) > maxRows {
		rows, complete = rows[:maxRows], false
	}
	for i := len(rows) - 1; i >= 0; i-- {
		entries = append(entries, DecodeRow(rows[i])...)
	}
	return entries, complete, nil
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
			DispatchedBy *wire.DispatchMarker  `json:"dispatchedBy"`
			Attachments  []wire.ChatAttachment `json:"attachments"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || (strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0) {
			return nil
		}
		e := Entry{Seq: r.Sequence, ChannelID: r.ChannelID, AtMs: r.AtUnixMs, Speaker: p.UserID, Kind: KindHuman, Text: p.Text}
		for _, a := range p.Attachments {
			if a.ID != "" {
				e.Attachments = append(e.Attachments, AttachmentRef{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size})
			}
		}
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
					SourceSeq   int64  `json:"sourceSeq"`
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
				Seq: r.Sequence, ChannelID: r.ChannelID, AtMs: r.AtUnixMs,
				Speaker: AgentSpeaker(mp.OwnerUserID, mp.Provider), Kind: KindAgent, Text: mp.Text,
				Owner: mp.OwnerUserID, Provider: mp.Provider, Session: p.AgentSessionID, Role: mp.Role, SourceSeq: mp.SourceSeq,
			})
		}
		return out
	}
	return nil
}
