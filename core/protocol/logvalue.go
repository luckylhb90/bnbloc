package protocol

import (
	"log/slog"

	"github.com/luckylhb90/bnbloc/core"
)

// The wire types that carry a secret (the exec token, profile env, MCP env
// and header values) log through core.LogRedacted, the one redaction rule.

func (s SandboxExecution) LogValue() slog.Value     { return core.LogRedacted(s) }
func (e Execution) LogValue() slog.Value            { return core.LogRedacted(e) }
func (r CreateSessionRequest) LogValue() slog.Value { return core.LogRedacted(r) }
func (r SendInputRequest) LogValue() slog.Value     { return core.LogRedacted(r) }
func (p AgentProfile) LogValue() slog.Value         { return core.LogRedacted(p) }
func (m McpServer) LogValue() slog.Value            { return core.LogRedacted(m) }
func (nv NameValue) LogValue() slog.Value           { return core.LogRedacted(nv) }
