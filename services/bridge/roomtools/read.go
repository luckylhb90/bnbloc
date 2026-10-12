package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
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
		Description: "Recent messages of the room (people's messages and agents' replies, across the room's channels), oldest first. " +
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
	if env.Session.current().JobID == "" {
		return toolError("read_room works only while you are answering a request in the room")
	}
	// The room's agent-readable channels (roomlog.History: never the people
	// chat), read back from the newest page.
	pages := readHistoryPages
	if in.Since <= 0 {
		pages = 1
	}
	pageRows := min(in.Limit, readHistoryPageMax)
	entries, _, err := roomlog.History(ctx, gatewayFetch(env), env.Session.Scope().RoomID, roomlog.HistoryQuery{
		After: in.Since, PageRows: pageRows, Pages: pages, MaxRows: readHistoryPages * readHistoryPageMax,
	})
	if err != nil {
		return refusal("reading the room", err)
	}
	var lines []roomLine
	for _, e := range entries {
		if e.Seq <= in.Since {
			continue
		}
		lines = append(lines, roomLine{
			Seq: e.Seq, Author: e.Speaker, Agent: e.Kind == roomlog.KindAgent, To: e.To, Via: e.Via,
			Text: truncateBytes(strings.TrimSpace(e.Text), maxReadTextBytes), AtMs: e.AtMs,
		})
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

// gatewayFetch reads gateway paths for roomlog through the tools' gateway
// client (the bridge's session, never the agent's).
func gatewayFetch(env *Env) roomlog.Fetch {
	return func(ctx context.Context, path string) ([]byte, error) {
		return env.Gateway.Do(ctx, http.MethodGet, path, nil)
	}
}
