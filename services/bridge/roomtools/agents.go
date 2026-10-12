package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// roomAgent is one agent of the room as the tools show it.
type roomAgent struct {
	Handle    string `json:"handle"`
	Owner     string `json:"owner"`
	Core      string `json:"core,omitempty"`
	Provider  string `json:"provider"`
	Online    bool   `json:"online"`
	Sandboxed bool   `json:"sandboxed,omitempty"`
	// You marks the agent this session drives (never a valid ask_agent
	// target).
	You bool `json:"you,omitempty"`
}

// roomAgents reads the room roster (GET /v1/rooms/{id}/agents: the agents
// members' bridges advertise as reachable now) and names each agent by a
// handle: "owner/provider", or "owner/core/provider" where one owner
// exposes the same provider on two cores.
func roomAgents(ctx context.Context, env *Env) ([]roomAgent, error) {
	scope := env.Session.Scope()
	raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/agents", nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Agents []wire.RosterAgent `json:"agents"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	pairs := map[string]int{}
	for _, a := range parsed.Agents {
		pairs[a.OwnerUserID+"/"+a.Provider]++
	}
	out := make([]roomAgent, 0, len(parsed.Agents))
	for _, a := range parsed.Agents {
		handle := a.OwnerUserID + "/" + a.Provider
		if pairs[handle] > 1 && a.Core != "" {
			handle = a.OwnerUserID + "/" + a.Core + "/" + a.Provider
		}
		out = append(out, roomAgent{
			Handle: handle, Owner: a.OwnerUserID, Core: a.Core, Provider: a.Provider,
			Online: true, Sandboxed: a.Sandboxed,
			You: a.OwnerUserID == scope.Owner && a.Provider == scope.Provider && (a.Core == "" || scope.Core == "" || a.Core == scope.Core),
		})
	}
	return out, nil
}

// resolveHandle finds the agent a handle names ("owner/provider" or
// "owner/core/provider", exactly).
func resolveHandle(agents []roomAgent, handle string) (roomAgent, bool) {
	handle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	parts := strings.Split(handle, "/")
	var matches []roomAgent
	for _, a := range agents {
		switch len(parts) {
		case 2:
			if a.Owner == parts[0] && a.Provider == parts[1] {
				matches = append(matches, a)
			}
		case 3:
			if a.Owner == parts[0] && a.Core == parts[1] && a.Provider == parts[2] {
				matches = append(matches, a)
			}
		}
	}
	if len(matches) != 1 {
		return roomAgent{}, false
	}
	return matches[0], true
}

func handles(agents []roomAgent) string {
	var hs []string
	for _, a := range agents {
		if !a.You {
			hs = append(hs, a.Handle)
		}
	}
	if len(hs) == 0 {
		return "none"
	}
	return strings.Join(hs, ", ")
}

func listRoomAgentsTool() Tool {
	return Tool{
		Name:  "list_room_agents",
		Title: "List the room's agents",
		Description: "List the agents you can ask in this room right now: each one's handle (pass it to ask_agent), owner, core and provider. " +
			"The agent marked you=true is you.",
		InputSchema: objectSchema(map[string]any{}),
		ReadOnly:    true,
		Call: func(ctx context.Context, env *Env, args json.RawMessage) Result {
			var in struct{}
			if err := decodeArgs(args, &in); err != nil {
				return toolError("list_room_agents takes no arguments")
			}
			agents, err := roomAgents(ctx, env)
			if err != nil {
				return refusal("listing the room's agents", err)
			}
			return Result{Value: map[string]any{
				"agents": agents,
				"limits": map[string]any{"maxDispatchesPerRequest": wire.MaxDispatchFanOut, "maxDepth": wire.MaxDispatchDepth},
			}}
		},
	}
}
