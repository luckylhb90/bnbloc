package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// signal sends an event to this room for the job the session runs
// (docs/prd/2026-10-26-rooms-work-together.md §2.3, D8): the room's tasks
// whose trigger takes it run on it (a flow's next station), the task whose
// run started this job never. It is sent as the job's requester, carrying
// the job's cause and chain, through the gateway's signals door. Action
// send only; request (asking a person) comes with the flow's requests.
func signalTool() Tool {
	return Tool{
		Name:  "signal",
		Title: "Signal",
		Description: "Tell this room that something happened, so the room's tasks that wait for it run (for example the next step of a flow). " +
			"action send: type is your event's name, such as \"draft.ready\" or \"review.changes_requested\" (never starting platform., room., component. or desktop.); " +
			"subject is optional (what it is about, for example a file name or an order id); data is an optional JSON object with the facts the next step needs (at most 16 KB). " +
			"It is sent for the person who asked you and names this request as its cause; the task that started this request does not run on it again. " +
			"Send it once, when the step is really done, never to report progress (post_progress does that).",
		InputSchema: objectSchema(map[string]any{
			"action":  map[string]any{"type": "string", "enum": []string{"send"}},
			"type":    map[string]any{"type": "string", "description": "The event's type, for example draft.ready."},
			"subject": map[string]any{"type": "string", "description": "What it is about (a file name, an order id)."},
			"data":    map[string]any{"type": "object", "description": "Facts the next step needs (JSON, at most 16 KB)."},
		}, "action", "type"),
		Call: func(ctx context.Context, env *Env, args json.RawMessage) Result {
			var in struct {
				Action  string          `json:"action"`
				Type    string          `json:"type"`
				Subject string          `json:"subject"`
				Data    json.RawMessage `json:"data"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return toolError("invalid arguments: %v", err)
			}
			if in.Action != "send" {
				return toolError("signal: action is send")
			}
			typ := strings.TrimSpace(in.Type)
			if typ == "" {
				return toolError("type is required")
			}
			if wire.ReservedEventType(typ) {
				return toolError("type %q is the platform's: types starting %s are only the platform's", typ, strings.Join(wire.ReservedEventTypePrefixes, ", "))
			}
			job := env.Session.current()
			if job.JobID == "" {
				return toolError("signal works only while you are answering a request in the room")
			}
			scope := env.Session.Scope()
			raw, err := env.Gateway.Do(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/signals",
				wire.SignalRequest{JobID: job.JobID, Type: typ, Subject: strings.TrimSpace(in.Subject), Data: in.Data})
			if err != nil {
				return refusal("sending the event", err)
			}
			var reply wire.MemberEventReply
			if err := json.Unmarshal(raw, &reply); err != nil {
				return toolError("sending the event: unexpected answer")
			}
			return Result{Value: map[string]any{"sent": reply.Event.Type, "id": reply.Event.ID, "tasksRun": reply.Triggered}}
		},
	}
}
