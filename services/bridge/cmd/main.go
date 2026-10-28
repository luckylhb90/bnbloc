// Command bnbloc-bridge connects a member's local agent cores to the bnbloc
// control-plane: it subscribes to room chat, drives agents, and pushes replies
// back into the room.
package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/luckylhb90/bnbloc/internal/buildinfo"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/services/bridge"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

func main() {
	// `bnbloc-bridge roomtools-stdio --url <endpoint>` is the room tools
	// stdio shim an agent without http MCP launches (bearer in
	// BNBLOC_ROOMTOOLS_TOKEN). Its stdout is the MCP channel, so it runs
	// before anything else writes.
	if len(os.Args) > 1 && os.Args[1] == "roomtools-stdio" {
		os.Exit(roomtools.RunStdioMain(os.Args[2:]))
	}
	// `bnbloc-bridge mcp-tool --dir <tools> --name <tool> -- <args>` starts
	// a stdio MCP tool the desktop materialized, by name (its stdout is the
	// MCP channel too).
	if len(os.Args) > 1 && os.Args[1] == bridge.MCPToolCommand {
		os.Exit(bridge.RunMCPToolMain(os.Args[2:]))
	}
	// The bridge serves no public HTTP, so its build identity goes to the log the
	// desktop collects (internal/buildinfo; stamped by build-bridge-sidecar.mjs).
	bi := buildinfo.Get("bnbloc-bridge")
	log.Printf("bnbloc-bridge: build commit=%q builtAt=%q", bi.Commit, bi.BuiltAt)
	gateway := strings.TrimRight(config.FromEnvRaw("BNBLOC_GATEWAY_BASE_URL"), "/")
	if gateway == "" {
		log.Fatal("bnbloc-bridge: BNBLOC_GATEWAY_BASE_URL is required")
	}
	owner := strings.TrimSpace(config.FromEnvRaw("BNBLOC_BRIDGE_OWNER"))
	tenantID := strings.TrimSpace(config.FromEnvRaw("BNBLOC_BRIDGE_TENANT"))
	if owner == "" {
		log.Fatal("bnbloc-bridge: BNBLOC_BRIDGE_OWNER is required (the member this bridge acts for)")
	}

	var rooms []string
	if r := strings.TrimSpace(config.FromEnvRaw("BNBLOC_BRIDGE_ROOMS")); r != "" {
		rooms = strings.Split(r, ",")
	}

	// Resolve the configured cores, running discovery to fill any endpoint
	// left unconfigured. Both the bridge and the gateway consume the same
	// coreset.FromEnv, so a member's cores are described once in the
	// environment.
	entries := coreset.FromEnv()

	// Every configured core is kept, reachable or not: a core whose daemon is
	// not up yet resolves later (the bridge re-runs discovery every roster
	// tick; a URL-declared protocol core discovers lazily on first use).
	for _, e := range entries {
		if !e.Endpoint.OK() {
			log.Printf("bnbloc-bridge: core %q has no reachable endpoint yet; it will be resolved when its daemon comes up", e.Name)
		}
	}
	if len(entries) == 0 {
		// Zero cores is a valid state, not a failure: cores exist only after a
		// super-admin installs them (or an operator sets BNBLOC_CORES/_URL).
		// The bridge keeps serving presence, sync, and heartbeats while driving
		// nothing; the desktop shell restarts it when the installed core set
		// changes, so installs take effect without manual intervention.
		log.Printf("bnbloc-bridge: no core configured yet — running idle (install a core or set BNBLOC_CORE_<NAME>_URL and restart)")
	}

	// The agent sandbox the desktop prepared (memory only; the admin token
	// is never logged and never handed to a core). The mode is always set;
	// the rest only while the sandbox is ready and the mode is not off.
	sandboxMode, err := bridge.ParseSandboxMode(config.FromEnvRaw("BNBLOC_SANDBOX_MODE"))
	if err != nil {
		log.Fatalf("bnbloc-bridge: %v", err)
	}
	// The names the member lets into their own sandboxed runs: an entry the
	// rule refuses is dropped and logged, never fatal (the desktop refuses
	// it on save; a stale settings file must not crash-loop the bridge).
	sandboxEnv, refusedEnv := bridge.ParseSandboxEnv(config.FromEnvRaw("BNBLOC_SANDBOX_ENV"))
	if len(refusedEnv) > 0 {
		log.Printf("bnbloc-bridge: BNBLOC_SANDBOX_ENV: ignoring %q (not an environment variable name or PREFIX* pattern a sandboxed run may receive)", refusedEnv)
	}
	// Where components contribute mounts to a sandboxed turn, and the guest
	// folders they may mount under (default: the member's home).
	home, _ := os.UserHomeDir()
	mountRoots, refusedRoots := bridge.ParseMountRoots(config.FromEnvRaw("BNBLOC_SANDBOX_MOUNT_ROOTS"), home)
	if len(refusedRoots) > 0 {
		log.Printf("bnbloc-bridge: BNBLOC_SANDBOX_MOUNT_ROOTS: ignoring %q (not an absolute, clean folder)", refusedRoots)
	}
	mountsDir := ""
	if stateDir := strings.TrimSpace(config.FromEnvRaw("BNBLOC_BRIDGE_STATE_DIR")); stateDir != "" {
		mountsDir = filepath.Join(stateDir, "mounts")
	}
	sandboxCfg := bridge.SandboxConfig{
		Mode:            sandboxMode,
		Env:             sandboxEnv,
		URL:             strings.TrimRight(strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_URL")), "/"),
		Token:           strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_TOKEN")),
		VMExec:          strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_VMEXEC")),
		Machine:         strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_MACHINE")),
		WorkspacesRoot:  strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_WORKSPACES_ROOT")),
		OwnMachine:      strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_OWN_MACHINE")),
		MountsDir:       mountsDir,
		MountsRoot:      strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_MOUNTS_ROOT")),
		MountGuestRoots: mountRoots,
		Account:         bridge.BridgeAccount(gateway, owner),
		HostProxy:       strings.TrimSpace(config.FromEnvRaw("BNBLOC_SANDBOX_HOST_PROXY")),
		PrivateHosts:    strings.FieldsFunc(config.FromEnvRaw("BNBLOC_SANDBOX_PRIVATE_HOSTS"), func(r rune) bool { return r == ',' || r == ' ' }),
	}
	// Memory only from here on: nothing this process spawns inherits the
	// sandbox admin bearer.
	_ = os.Unsetenv("BNBLOC_SANDBOX_TOKEN")

	b := bridge.New(bridge.Config{
		Sandbox:        sandboxCfg,
		GatewayBaseURL: gateway,
		// GatewayToken is a static bearer for gateway calls. Empty (the default)
		// makes the bridge mint an owner session token via login so its room SSE
		// + activity posts carry room scope. Set it when the gateway uses an
		// enterprise IdP that rejects the dev login (remote deployments).
		GatewayToken: config.FromEnvRaw("BNBLOC_GATEWAY_TOKEN"),
		// RefreshToken is the owner's refresh token, handed over by the desktop
		// after sign-in; required on SSO-only gateways (no direct login).
		RefreshToken:      config.FromEnvRaw("BNBLOC_GATEWAY_REFRESH_TOKEN"),
		OwnerSubject:      owner,
		BridgeID:          config.FromEnvRaw("BNBLOC_BRIDGE_ID"),
		AppInstanceID:     config.FromEnvRaw("BNBLOC_BRIDGE_APP_INSTANCE_ID"),
		Version:           config.FromEnvRaw("BNBLOC_BRIDGE_VERSION"),
		Tenant:            tenantID,
		DisabledProviders: parseDisabledProviders(config.FromEnvRaw("BNBLOC_BRIDGE_DISABLED_PROVIDERS")),
		ServeAllOwners:    config.ParseBool(config.FromEnvRaw("BNBLOC_BRIDGE_SERVE_ALL_OWNERS"), false),
		Rooms:             rooms,
		RelayBaseURL:      config.FromEnvRaw("BNBLOC_RELAY_BASE_URL"),
		SyncEngine:        config.FromEnvRaw("BNBLOC_BRIDGE_SYNC_ENGINE"),
		SyncSecret:        config.FromEnvRaw("BNBLOC_SYNC_SECRET"),
		Cores:             entries,
		// MCP tools: the desktop's materialized stdio binaries, and the
		// member's own component settings and sign-ins (never sent to the
		// gateway).
		MCPToolsDir:          strings.TrimSpace(config.FromEnvRaw("BNBLOC_MCP_TOOLS_DIR")),
		MCPToolsStore:        strings.TrimSpace(config.FromEnvRaw("BNBLOC_MCP_TOOLS_STORE")),
		ComponentSettingsDir: strings.TrimSpace(config.FromEnvRaw("BNBLOC_COMPONENT_SETTINGS_DIR")),
		MCPSignInsDir:        strings.TrimSpace(config.FromEnvRaw("BNBLOC_MCP_SIGN_INS_DIR")),
		// Which workspace each agent of a room uses, kept across restarts
		// (the desktop: the member's own directory; empty = memory only).
		StateDir: strings.TrimSpace(config.FromEnvRaw("BNBLOC_BRIDGE_STATE_DIR")),
		// How long one turn may run (0 = no bound: a turn runs while its
		// agent works); a bound is sent to the core as the turn's deadline.
		MaxTurn: config.Duration("BNBLOC_BRIDGE_MAX_TURN", 0),
		// Deployment tuning floors for per-drive context (0 => built-in default).
		WindowChars:       config.GetInt("BNBLOC_BRIDGE_CONTEXT_WINDOW_CHARS", 0),
		ContextRecentRows: config.GetInt("BNBLOC_BRIDGE_CONTEXT_RECENT_ROWS", 0),
	})

	// Room tools: the loopback MCP server every driven agent session gets
	// (list the room's agents, dispatch work to them). The stdio shim is this
	// executable. A server that cannot listen leaves agents without room
	// tools; driving is unaffected.
	shim, err := os.Executable()
	if err != nil {
		log.Printf("bnbloc-bridge: room tools stdio shim unavailable: %v", err)
		shim = ""
	}
	// Stdio MCP tools start through this executable too, by name: a tool's
	// update then never restarts an agent's conversation.
	b.UseToolLauncher(shim)
	if stopRoomTools, err := b.EnableRoomTools(shim); err != nil {
		log.Printf("bnbloc-bridge: room tools disabled: %v", err)
	} else {
		defer stopRoomTools()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// When embedded as a desktop sidecar the shell sets BNBLOC_BRIDGE_SUPERVISED
	// and keeps stdin open for the child's lifetime. If the shell dies — even by
	// SIGKILL, where its graceful sidecar-kill cannot run — the stdin write end
	// closes and we read EOF, so the bridge exits instead of leaking. Standalone
	// (docker) runs leave the flag unset and keep the pure signal lifecycle.
	if config.FromEnvRaw("BNBLOC_BRIDGE_SUPERVISED") != "" {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop() // parent gone (stdin EOF): trigger graceful shutdown
		}()
	}

	b.Run(ctx)
}

// parseDisabledProviders decodes BNBLOC_BRIDGE_DISABLED_PROVIDERS: a
// comma-separated list of "core/provider" keys the user turned off.
func parseDisabledProviders(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, tok := range strings.Split(raw, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out[tok] = true
		}
	}
	return out
}
