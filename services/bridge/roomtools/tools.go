package roomtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Instructions tell the agent what the room tools are for. They ride the
// MCP initialize/discover answer and the session profile, and stay the same
// for every session.
const Instructions = `You are working in a bnbloc room shared by people and their agents. The bnbloc-room tools let you see the room's agents and ask one of them to take part of your task:
- list_room_agents: who you can ask (handles such as "jack/codex").
- ask_agent: send a task to one agent and wait for its reply and deliverables. The task is posted in the room on behalf of the person who asked you, so it runs under their authority (another member's agent may refuse without their authorization). Give a self-contained task.
- room_status: the state of tasks you dispatched (use the handle ask_agent returns when it stops waiting).
- read_room: recent messages of this conversation.
- post_progress: post a short progress line of your current request to the room (once per phase of a longer task).
- save_component, get_component, list_components: save an expert, skill pack or prompt pack you drafted in your workspace (a directory with component.json) to the Library of the person who asked you, only after they confirm; read or list theirs to improve one.
- publish_component: after they confirm, ask their organization's admins to publish one of theirs to the organization (scope org; the admins approve it), or take it back (scope personal).
Dispatching is bounded: at most 8 tasks per request you are working on, at most 3 hops deep, never to yourself or to an agent already working on this request.`

// Tool is one room tool: its MCP descriptor and its implementation.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	// ReadOnly marks a tool that changes nothing (an MCP annotation hint).
	ReadOnly bool
	Call     func(ctx context.Context, env *Env, args json.RawMessage) Result
}

// Env is what a tool call runs with: the calling session, the bridge's
// gateway client, the server configuration and a progress reporter.
type Env struct {
	Session  *Session
	Gateway  Gateway
	Config   Config
	Progress func(progress float64, message string)
}

// Result is a tool's answer: a JSON-able value, or an error message the
// agent reads (an MCP tool error, not a protocol error).
type Result struct {
	Value any
	Err   string
}

func toolError(format string, args ...any) Result {
	return Result{Err: fmt.Sprintf(format, args...)}
}

// wire renders the MCP CallToolResult: the value as structured content plus
// its JSON text for clients that read only text content.
func (r Result) wire() map[string]any {
	if r.Err != "" {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": r.Err}}, "isError": true}
	}
	text, err := json.Marshal(r.Value)
	if err != nil {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "result encoding failed"}}, "isError": true}
	}
	out := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "isError": false}
	if _, isObject := r.Value.(map[string]any); isObject {
		out["structuredContent"] = r.Value
	}
	return out
}

// Registry is the set of tools a server offers. Tools are registered once
// at construction.
type Registry struct {
	mu     sync.RWMutex
	tools  []Tool
	byName map[string]int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byName: map[string]int{}} }

// Register adds a tool; a duplicate or unnamed tool is refused.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" || t.Call == nil {
		return errors.New("roomtools: a tool needs a name and an implementation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[t.Name]; dup {
		return fmt.Errorf("roomtools: tool %q already registered", t.Name)
	}
	r.byName[t.Name] = len(r.tools)
	r.tools = append(r.tools, t)
	return nil
}

// Lookup finds a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.byName[name]
	if !ok {
		return Tool{}, false
	}
	return r.tools[i], true
}

// Descriptors are the tools/list entries, in registration order.
func (r *Registry) Descriptors() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]map[string]any, 0, len(r.tools))
	for _, t := range r.tools {
		d := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema}
		if t.Title != "" {
			d["title"] = t.Title
		}
		d["annotations"] = map[string]any{"readOnlyHint": t.ReadOnly, "openWorldHint": false}
		out = append(out, d)
	}
	return out
}

// DefaultRegistry holds the room tools: dispatch (step 2), content
// authoring (step 5) and team progress (step 6).
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, t := range []Tool{
		listRoomAgentsTool(), askAgentTool(), roomStatusTool(), readRoomTool(),
		saveComponentTool(), getComponentTool(), listComponentsTool(), publishComponentTool(), tryDraftTool(),
		postProgressTool(),
	} {
		if err := r.Register(t); err != nil {
			panic(err) // a static registration bug
		}
	}
	return r
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func httpStatusText(status int) string {
	return fmt.Sprintf("%d %s", status, http.StatusText(status))
}

// refusal renders a gateway refusal ({code, error} body) for the agent.
func refusal(what string, err error) Result {
	var se *StatusError
	if errors.As(err, &se) {
		var body struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(se.Body, &body) == nil && body.Error != "" {
			if body.Code != "" {
				return toolError("%s refused (%s): %s", what, body.Code, body.Error)
			}
			return toolError("%s refused: %s", what, body.Error)
		}
		if msg := strings.TrimSpace(string(se.Body)); msg != "" && len(msg) < 300 {
			return toolError("%s failed (%s): %s", what, httpStatusText(se.Status), msg)
		}
		return toolError("%s failed (%s)", what, httpStatusText(se.Status))
	}
	return toolError("%s failed: %v", what, err)
}
