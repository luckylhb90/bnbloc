package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Bounds of what ask_agent / room_status read back.
const (
	maxReplyBytes      = 32 << 10
	replyHistoryPage   = 100
	replyHistoryPages  = 5
	maxDeliverables    = 50
	maxTaskBytes       = wire.MaxChatTextBytes
	maxStatusDispatchs = 20
)

// outcome is the state of one dispatched task as the tools report it.
type outcome struct {
	Handle        string        `json:"handle"`
	Agent         string        `json:"agent"`
	Status        string        `json:"status"`
	Settled       bool          `json:"settled"`
	FailureReason string        `json:"failureReason,omitempty"`
	Error         string        `json:"error,omitempty"`
	Reply         string        `json:"reply,omitempty"`
	Deliverables  []deliverable `json:"deliverables,omitempty"`
	Note          string        `json:"note,omitempty"`
}

// deliverable is one file the dispatched agent published for the task.
type deliverable struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Link string `json:"link"`
}

func settled(status string) bool {
	switch status {
	case "replied", "failed", "timed_out", "cancelled":
		return true
	}
	return false
}

func askAgentTool() Tool {
	return Tool{
		Name:  "ask_agent",
		Title: "Ask another agent of the room",
		Description: "Send a task to another agent of this room and wait for it to finish. The task is posted in the room on behalf of the person who asked you " +
			"(visible to everyone, attributed to you) and runs under that person's authority. Returns the agent's reply and the files it delivered. " +
			"If the agent is still working when the wait ends, returns its handle: check it later with room_status.",
		InputSchema: objectSchema(map[string]any{
			"handle":       map[string]any{"type": "string", "description": `The agent's handle from list_room_agents, e.g. "jack/codex".`},
			"task":         map[string]any{"type": "string", "description": "A self-contained task: the agent sees the room's conversation, not your private context."},
			"expert":       map[string]any{"type": "string", "description": `Optional expert (role) for the agent to wear: its component id, optionally "@version" (it must be usable by the person who asked you).`},
			"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "description": "How long to wait for the result (default 600, at most 1200). 0 returns at once with the handle."},
		}, "handle", "task"),
		Call: askAgent,
	}
}

func askAgent(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		Handle      string `json:"handle"`
		Task        string `json:"task"`
		Expert      string `json:"expert"`
		WaitSeconds *int   `json:"wait_seconds"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	in.Task = strings.TrimSpace(in.Task)
	in.Expert = strings.TrimSpace(in.Expert)
	switch {
	case in.Task == "":
		return toolError("task is required")
	case len(in.Task) > maxTaskBytes:
		return toolError("task is too long (at most %d bytes)", maxTaskBytes)
	}
	job := env.Session.current()
	if job.JobID == "" {
		return toolError("ask_agent works only while you are answering a request in the room")
	}
	agents, err := roomAgents(ctx, env)
	if err != nil {
		return refusal("listing the room's agents", err)
	}
	target, ok := resolveHandle(agents, in.Handle)
	if !ok {
		return toolError("no agent %q in this room right now; available: %s", in.Handle, handles(agents))
	}
	if target.You {
		return toolError("%s is you; ask another agent", target.Handle)
	}
	scope := env.Session.Scope()
	raw, err := env.Gateway.Do(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/dispatch", wire.DispatchRequest{
		ParentJobID: job.JobID,
		Recipient:   wire.DispatchRecipient{OwnerID: target.Owner, Provider: target.Provider, Core: target.Core},
		Text:        in.Task,
		Expert:      expertRef(in.Expert),
		ClientMsgID: newClientMsgID(),
	})
	if err != nil {
		return refusal("dispatch to "+target.Handle, err)
	}
	var reply wire.DispatchReply
	if err := json.Unmarshal(raw, &reply); err != nil || reply.JobID == "" {
		return toolError("dispatch to %s: unexpected answer", target.Handle)
	}
	env.Session.remember(dispatched{JobID: reply.JobID, Handle: target.Handle, Sequence: reply.Sequence})

	wait := env.Config.DefaultWait
	if in.WaitSeconds != nil {
		wait = time.Duration(*in.WaitSeconds) * time.Second
	}
	if wait > env.Config.MaxWait {
		wait = env.Config.MaxWait
	}
	run, err := waitSettled(ctx, env, reply.JobID, target.Handle, wait)
	if err != nil {
		return Result{Value: map[string]any{
			"handle": reply.JobID, "agent": target.Handle, "status": "queued", "settled": false,
			"note": "dispatched; reading its state failed (" + err.Error() + "); check it with room_status",
		}}
	}
	return Result{Value: toMap(describe(ctx, env, run, target.Handle))}
}

// expertRef parses "id" or "id@version" (nil for none).
func expertRef(s string) *wire.ChatExpert {
	if s == "" {
		return nil
	}
	id, version, _ := strings.Cut(s, "@")
	return &wire.ChatExpert{ID: strings.TrimSpace(id), Version: strings.TrimSpace(version)}
}

// waitSettled polls the dispatched job until it settles or wait ends,
// reporting progress on the request's stream. It returns the latest run.
func waitSettled(ctx context.Context, env *Env, jobID, handle string, wait time.Duration) (wire.AgentRun, error) {
	start := time.Now()
	deadline := start.Add(wait)
	var last wire.AgentRun
	for {
		run, err := readRun(ctx, env, jobID)
		if err != nil {
			if ctx.Err() != nil || last.JobID == "" {
				return run, err
			}
			run = last // a transient read failure keeps waiting on the last state
		}
		last = run
		if settled(run.Status) || !time.Now().Before(deadline) {
			return run, nil
		}
		env.Progress(time.Since(start).Seconds(), handle+" is "+strings.ReplaceAll(run.Status, "_", " "))
		timer := time.NewTimer(min(env.Config.PollInterval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return run, nil
		case <-timer.C:
		}
	}
}

// readRun reads one run of the session's room (GET /v1/rooms/{id}/jobs/{jobId}).
func readRun(ctx context.Context, env *Env, jobID string) (wire.AgentRun, error) {
	scope := env.Session.Scope()
	raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return wire.AgentRun{}, err
	}
	var run wire.AgentRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return wire.AgentRun{}, err
	}
	return run, nil
}

// describe renders a run's outcome: for a settled run the reply text (from
// the room history) and the deliverables published for its request.
func describe(ctx context.Context, env *Env, run wire.AgentRun, handle string) outcome {
	out := outcome{Handle: run.JobID, Agent: handle, Status: run.Status, Settled: settled(run.Status), FailureReason: run.FailureReason, Error: run.Error}
	if !out.Settled {
		out.Note = "still " + strings.ReplaceAll(run.Status, "_", " ") + "; check it later with room_status"
		return out
	}
	out.Reply = replyText(ctx, env, run)
	if env.Config.DeliverableSettle > 0 && run.Status == "replied" {
		// The bridge publishes a turn's files right after its reply.
		select {
		case <-ctx.Done():
		case <-time.After(env.Config.DeliverableSettle):
		}
	}
	out.Deliverables = deliverables(ctx, env, run)
	return out
}

// replyText collects the dispatched agent's replies to the request from
// the room history: agent_messages after the request (from the first
// reply's sequence when known), from the run's agent session, up to the
// run's finish. Falls back to the run record's reply preview.
func replyText(ctx context.Context, env *Env, run wire.AgentRun) string {
	preview := ""
	start := run.Request.MessageSeq + 1
	if run.Reply != nil {
		preview = run.Reply.Text
		if run.Reply.RoomSeq > 0 {
			start = run.Reply.RoomSeq
		}
	}
	sessionID, _ := run.Diagnostics["sessionId"].(string)
	rows, err := historySince(ctx, env, run.ChannelID, start-1, replyHistoryPage, replyHistoryPages)
	if err != nil {
		return preview
	}
	var b strings.Builder
	for _, row := range rows {
		if row.Kind != wire.KindAgentMessages || (run.Phases.FinishedAtMs > 0 && row.AtUnixMs > run.Phases.FinishedAtMs) {
			continue
		}
		var batch struct {
			AgentSessionID string `json:"agentSessionId"`
			Messages       []struct {
				Payload struct {
					Role        string `json:"role"`
					Text        string `json:"text"`
					OwnerUserID string `json:"ownerUserId"`
					Provider    string `json:"provider"`
				} `json:"payload"`
			} `json:"messages"`
		}
		if json.Unmarshal(row.Payload, &batch) != nil || (sessionID != "" && batch.AgentSessionID != sessionID) {
			continue
		}
		for _, m := range batch.Messages {
			p := m.Payload
			if p.Role != "assistant" || !strings.EqualFold(p.OwnerUserID, run.Agent.Owner) || !strings.EqualFold(p.Provider, run.Agent.Provider) || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(strings.TrimSpace(p.Text))
		}
	}
	if b.Len() == 0 {
		return preview
	}
	return truncateBytes(b.String(), maxReplyBytes)
}

// historyRow is one room history row (GET /v1/rooms/{id}/history).
type historyRow struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
	AtUnixMs int64           `json:"atUnixMs"`
}

// historySince pages a channel's history back from the newest page until
// it reaches a sequence <= after, returning the rows after it, oldest
// first (bounded by pages).
func historySince(ctx context.Context, env *Env, channelID string, after int64, pageSize, pages int) ([]historyRow, error) {
	scope := env.Session.Scope()
	var out []historyRow
	before := int64(0)
	for page := 0; page < pages; page++ {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}}
		if channelID != "" {
			q.Set("channelId", channelID)
		}
		if before > 0 {
			q.Set("before", strconv.FormatInt(before, 10))
		}
		raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/history?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Messages []historyRow `json:"messages"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		done := len(parsed.Messages) < pageSize
		for _, m := range parsed.Messages {
			if m.Sequence > after {
				out = append(out, m)
			} else {
				done = true
			}
			if before == 0 || m.Sequence < before {
				before = m.Sequence
			}
		}
		if done || before <= 1 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

// deliverables lists what the dispatched agent published for the request
// (GET /v1/rooms/{id}/artifacts?sourceSeq=: versions link back to the
// prompting message).
func deliverables(ctx context.Context, env *Env, run wire.AgentRun) []deliverable {
	if run.Request.MessageSeq <= 0 {
		return nil
	}
	scope := env.Session.Scope()
	q := url.Values{"sourceSeq": {strconv.FormatInt(run.Request.MessageSeq, 10)}, "limit": {strconv.Itoa(maxDeliverables)}}
	raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/artifacts?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	var parsed struct {
		Artifacts []struct {
			ArtifactID     string `json:"artifactId"`
			Kind           string `json:"kind"`
			Title          string `json:"title"`
			FileName       string `json:"fileName"`
			CurrentVersion int    `json:"currentVersion"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	out := make([]deliverable, 0, len(parsed.Artifacts))
	for _, a := range parsed.Artifacts {
		name := a.FileName
		if name == "" {
			name = a.Title
		}
		link := strings.TrimRight(env.Config.GatewayBaseURL, "/") + "/v1/rooms/" + url.PathEscape(scope.RoomID) + "/artifacts/" + url.PathEscape(a.ArtifactID)
		if a.CurrentVersion > 0 {
			link += "/versions/" + strconv.Itoa(a.CurrentVersion) + "/content"
		}
		out = append(out, deliverable{Name: name, Kind: a.Kind, Link: link})
	}
	return out
}

func roomStatusTool() Tool {
	return Tool{
		Name:        "room_status",
		Title:       "Status of dispatched tasks",
		Description: "The state of the tasks you dispatched with ask_agent. With a handle: that task, with its reply and deliverables once finished. Without: every task you dispatched in this session.",
		InputSchema: objectSchema(map[string]any{
			"handle": map[string]any{"type": "string", "description": "A handle ask_agent returned (optional)."},
		}),
		ReadOnly: true,
		Call: func(ctx context.Context, env *Env, args json.RawMessage) Result {
			var in struct {
				Handle string `json:"handle"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return toolError("invalid arguments: %v", err)
			}
			if h := strings.TrimSpace(in.Handle); h != "" {
				d, ok := env.Session.owns(h)
				if !ok {
					return toolError("no task %q was dispatched from this session", h)
				}
				run, err := readRun(ctx, env, d.JobID)
				if err != nil {
					return refusal("reading the task", err)
				}
				return Result{Value: toMap(describe(ctx, env, run, d.Handle))}
			}
			ds := env.Session.dispatches()
			if len(ds) > maxStatusDispatchs {
				ds = ds[len(ds)-maxStatusDispatchs:]
			}
			tasks := make([]outcome, 0, len(ds))
			for _, d := range ds {
				o := outcome{Handle: d.JobID, Agent: d.Handle, Status: "unknown"}
				if run, err := readRun(ctx, env, d.JobID); err == nil {
					o.Status, o.Settled, o.FailureReason, o.Error = run.Status, settled(run.Status), run.FailureReason, run.Error
				}
				tasks = append(tasks, o)
			}
			return Result{Value: map[string]any{"tasks": tasks}}
		},
	}
}

// toMap turns a struct into the object a structured tool result carries.
func toMap(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…(truncated)"
}
