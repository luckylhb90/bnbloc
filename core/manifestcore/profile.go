package manifestcore

import (
	"encoding/json"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// Agent profile template vars (capability agentProfile). A manifest maps a
// session's profile onto its daemon through createSession body vars:
//
//   - {instructions} — the profile's instructions as a string;
//   - "{agentProfile|json}" — the whole profile in the Core Protocol wire
//     shape (protocol.AgentProfile) as a JSON object;
//   - "{skills|json}", "{mcpServers|json}", "{env|json}" — its parts.
//
// A manifest whose createSession body references any of them advertises
// agentProfile (Supports); the daemon decides what it can apply. Env and MCP
// header values are secrets the daemon must not log or persist.
var profileVarNames = []string{"instructions", "agentProfile", "skills", "mcpServers", "env"}

// profileVars renders a profile into the vars above ("" for each when nil).
func profileVars(p *core.AgentProfile) vars {
	out := vars{}
	for _, name := range profileVarNames {
		out[name] = ""
	}
	if p == nil {
		return out
	}
	w := protocol.WireProfile(p)
	out["instructions"] = w.Instructions
	out["agentProfile"] = marshalVar(w)
	if len(w.Skills) > 0 {
		out["skills"] = marshalVar(w.Skills)
	}
	if len(w.McpServers) > 0 {
		out["mcpServers"] = marshalVar(w.McpServers)
	}
	if len(w.Env) > 0 {
		out["env"] = marshalVar(w.Env)
	}
	return out
}

func marshalVar(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// createSessionTakesProfile reports whether a createSession body template
// references a profile var, i.e. the manifest maps agent profiles.
func createSessionTakesProfile(body json.RawMessage) bool {
	s := string(body)
	for _, name := range profileVarNames {
		if strings.Contains(s, "{"+name+"}") || strings.Contains(s, "{"+name+"|") {
			return true
		}
	}
	return false
}
