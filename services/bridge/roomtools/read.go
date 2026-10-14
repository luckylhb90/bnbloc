package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
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

// readAuthor names who wrote read_room's transcript inside its fence.
const readAuthor = "the room's people and agents (each line names its author)"

func readRoomTool() Tool {
	return Tool{
		Name:  "read_room",
		Title: "Read the conversation",
		Description: "Recent messages of this conversation (people's messages and agents' replies), oldest first, " +
			"one line each: \"#<seq> <author>: <text>\". Pass since (a seq from an earlier read) to get only newer messages.",
		InputSchema: objectSchema(map[string]any{
			"since": map[string]any{"type": "integer", "minimum": 0, "description": "Only messages after this seq."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadLimit, "description": "At most this many messages (default " + strconv.Itoa(defaultReadLimit) + ")."},
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
	// A channel is a context boundary: the agent reads the conversation it
	// is answering in (the others' records are in .room/channels/).
	room := env.Session.Scope().RoomID
	list, listed := roomlog.AgentChannelList(ctx, gatewayFetch(env), room)
	own, ok := roomlog.ChannelOf(list, env.Session.current().ChannelID)
	if listed && !ok {
		return toolError("the conversation you are answering in is not one you may read")
	}
	entries, _, err := roomlog.History(ctx, gatewayFetch(env), room, roomlog.HistoryQuery{
		Only: own.ID, After: in.Since, PageRows: pageRows, Pages: pages, MaxRows: readHistoryPages * readHistoryPageMax,
	})
	if err != nil {
		return refusal("reading the room", err)
	}
	var lines []roomlog.Entry
	for _, e := range entries {
		if e.Seq <= in.Since {
			continue
		}
		e.Text = roomlog.Truncate(strings.TrimSpace(e.Text), maxReadTextBytes)
		lines = append(lines, e)
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
	var transcript strings.Builder
	for _, e := range lines {
		latest = max(latest, e.Seq)
		transcript.WriteString("#" + strconv.FormatInt(e.Seq, 10) + " " + roomlog.Line(e))
	}
	out := map[string]any{"latestSeq": latest, "more": more && in.Since > 0}
	if transcript.Len() > 0 {
		// What people and other agents wrote: data, never instructions.
		out["transcript"] = roomlog.Fence(readAuthor, transcript.String())
	}
	return Result{Value: out}
}

// gatewayFetch reads gateway paths for roomlog through the tools' gateway
// client (the bridge's session, never the agent's).
func gatewayFetch(env *Env) roomlog.Fetch {
	return func(ctx context.Context, path string) ([]byte, error) {
		return env.Gateway.Do(ctx, http.MethodGet, path, nil)
	}
}
