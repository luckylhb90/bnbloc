package roomtools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// read_room bounds.
const (
	defaultReadLimit   = 30
	maxReadLimit       = 100
	maxReadTextBytes   = 4 << 10
	readHistoryPages   = 3
	readHistoryPageMax = 100
)

// roomLine is one message as read_room shows it.
type roomLine struct {
	Seq    int64  `json:"seq"`
	Author string `json:"author"`
	// Agent is set for an agent's reply.
	Agent bool `json:"agent,omitempty"`
	// To is the agent a person's message addressed ("" = people only).
	To string `json:"to,omitempty"`
	// Via is the agent that dispatched this message on the author's behalf.
	Via  string `json:"via,omitempty"`
	Text string `json:"text"`
	AtMs int64  `json:"atMs,omitempty"`
}

func readRoomTool() Tool {
	return Tool{
		Name:  "read_room",
		Title: "Read the conversation",
		Description: "Recent messages of the conversation you are answering in (people's messages and agents' replies), oldest first. " +
			"Pass since (a seq from an earlier read) to get only newer messages.",
		InputSchema: objectSchema(map[string]any{
			"since": map[string]any{"type": "integer", "minimum": 0, "description": "Only messages after this seq."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadLimit, "description": "At most this many messages (default 30)."},
		}),
		ReadOnly: true,
		Call:     readRoom,
	}
}

func readRoom(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		Since int64 `json:"since"`
		Limit int   `json:"limit"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	if in.Limit <= 0 {
		in.Limit = defaultReadLimit
	}
	if in.Limit > maxReadLimit {
		in.Limit = maxReadLimit
	}
	// The conversation is the channel of the job being answered. It is never
	// the people chat (no agent job exists there), so this read can never
	// show agents the human-only conversation.
	job := env.Session.current()
	if job.JobID == "" {
		return toolError("read_room works only while you are answering a request in the room")
	}
	pages := readHistoryPages
	if in.Since <= 0 {
		pages = 1
	}
	rows, err := historySince(ctx, env, job.ChannelID, in.Since, min(in.Limit, readHistoryPageMax), pages)
	if err != nil {
		return refusal("reading the room", err)
	}
	var lines []roomLine
	for _, row := range rows {
		lines = append(lines, renderRow(row)...)
	}
	more := len(lines) > in.Limit
	switch {
	case more && in.Since > 0:
		// Reading forward: the oldest messages after since first; the next
		// read continues from latestSeq.
		lines = lines[:in.Limit]
	case more:
		lines = lines[len(lines)-in.Limit:]
	}
	latest := in.Since
	for _, l := range lines {
		latest = max(latest, l.Seq)
	}
	return Result{Value: map[string]any{"messages": lines, "latestSeq": latest, "more": more && in.Since > 0}}
}

// renderRow turns one history row into the lines it shows.
func renderRow(row historyRow) []roomLine {
	switch row.Kind {
	case "chat_message":
		var p struct {
			UserID    string `json:"userId"`
			Text      string `json:"text"`
			Recipient *struct {
				OwnerID  string `json:"ownerId"`
				Provider string `json:"provider"`
			} `json:"recipient"`
			DispatchedBy *wire.DispatchMarker `json:"dispatchedBy"`
		}
		if json.Unmarshal(row.Payload, &p) != nil {
			return nil
		}
		line := roomLine{Seq: row.Sequence, Author: p.UserID, Text: truncateBytes(strings.TrimSpace(p.Text), maxReadTextBytes), AtMs: row.AtUnixMs}
		if p.Recipient != nil && p.Recipient.Provider != "" {
			line.To = p.Recipient.OwnerID + "/" + p.Recipient.Provider
		}
		if p.DispatchedBy != nil {
			line.Via = p.DispatchedBy.Via
		}
		return []roomLine{line}
	case wire.KindAgentMessages:
		var batch struct {
			Messages []struct {
				Payload struct {
					Role        string `json:"role"`
					Text        string `json:"text"`
					OwnerUserID string `json:"ownerUserId"`
					Provider    string `json:"provider"`
				} `json:"payload"`
			} `json:"messages"`
		}
		if json.Unmarshal(row.Payload, &batch) != nil {
			return nil
		}
		var out []roomLine
		for _, m := range batch.Messages {
			p := m.Payload
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			author := p.Provider
			if p.OwnerUserID != "" {
				author = p.OwnerUserID + "/" + p.Provider
			}
			out = append(out, roomLine{Seq: row.Sequence, Author: author, Agent: p.Role == "assistant", Text: truncateBytes(strings.TrimSpace(p.Text), maxReadTextBytes), AtMs: row.AtUnixMs})
		}
		return out
	}
	return nil
}
