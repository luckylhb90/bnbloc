# Core Protocol v1 conformance

Use this package from a Go test to verify either an in-process `core.Provider` or
a live daemon base URL:

```go
func TestMyCoreConformance(t *testing.T) {
	conformance.RunLive(t, conformance.Config{
		Endpoint: core.Endpoint{BaseURL: "http://127.0.0.1:4545", Token: "..."},
		TargetID: "local:codex",
		Prompt:   "Say hello from conformance.",
	})
}
```

For in-process providers, pass the provider and endpoint:

```go
conformance.RunProvider(t, myProvider, conformance.Config{
	Endpoint: core.Endpoint{BaseURL: server.URL},
	TargetID: "local:codex",
	Prompt:   "Say hello from conformance.",
})
```

The suite exercises discovery/version negotiation, every required provider
method, protocol error-envelope parsing for live daemons, and every optional
capability the implementation advertises or exposes through Go optional
interfaces. Not advertising an optional capability is acceptable; advertising it
and failing it is a conformance failure.

Two optional capabilities carry contract checks beyond "it answers":

- `writeFile`: a `.room/` file and an agent-config file
  (`.agents/skills/…`) are written, and a path outside `.room/` and
  `core.AgentConfigPaths` must be refused (`core.WorkspaceFileAllowed`).
- `agentProfile`: a session created with a profile (instructions, a skill,
  an env var) must start.
