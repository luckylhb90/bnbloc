package conformance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

type Config struct {
	Endpoint    core.Endpoint
	HTTPClient  *http.Client
	TargetID    string
	Prompt      string
	Input       string
	FilePath    string
	FileContent string
	Timeout     time.Duration
}

func RunLive(t *testing.T, cfg Config) {
	t.Helper()
	if err := CheckLive(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func CheckLive(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	provider := protocol.NewClient("core-protocol", cfg.HTTPClient)
	d, err := provider.Discovery(ctx, cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("discover live protocol provider: %w", err)
	}
	if err := checkDiscoveryCapabilities(d); err != nil {
		return err
	}
	if err := CheckProvider(ctx, provider, cfg); err != nil {
		return err
	}
	if protocol.HasCapability(d, protocol.CapabilityWriteFile) {
		if err := checkLiveWriteFileRefusal(ctx, provider, cfg); err != nil {
			return err
		}
		if err := checkLiveLargeWrite(ctx, provider, cfg, d); err != nil {
			return err
		}
	}
	return checkLiveErrorEnvelope(ctx, provider, cfg)
}

// checkLiveWriteFileRefusal sends the daemon a write outside
// core.WorkspaceFileAllowed directly (the protocol client refuses it before
// the wire): the daemon must refuse it too, with a 4xx.
func checkLiveWriteFileRefusal(ctx context.Context, provider *protocol.Client, cfg Config) error {
	workspaceID, err := provider.CreateWorkspace(ctx, cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("CreateWorkspace: %w", err)
	}
	raw := core.REST{Name: "conformance", HTTP: cfg.HTTPClient}
	_, err = raw.Do(ctx, http.MethodPut, cfg.Endpoint, protocol.WorkspaceFilePath(workspaceID, "conformance-outside.txt"), protocol.WriteWorkspaceFileRequest{Content: "x"})
	var he *core.HTTPError
	if !errors.As(err, &he) || he.Status < 400 || he.Status >= 500 {
		return fmt.Errorf("daemon accepted (or failed with %v) a write outside .room/ and the agent-config paths; want a 4xx refusal", err)
	}
	return nil
}

// checkLiveLargeWrite writes the largest file a writeFile daemon must
// accept: a chat attachment at its cap (protocol.MinWriteFileBytes), or the
// largest file within the daemon's stated limits.maxInputBytes. The bridge
// hands each attachment to the agent's workspace with one such write.
func checkLiveLargeWrite(ctx context.Context, provider *protocol.Client, cfg Config, d protocol.Discovery) error {
	n := int64(protocol.MinWriteFileBytes)
	if limit := d.Limits.MaxInputBytes; limit > 0 {
		n = min(n, protocol.MaxWriteFileBytes(limit))
	}
	workspaceID, err := provider.CreateWorkspace(ctx, cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("CreateWorkspace: %w", err)
	}
	if err := provider.WriteWorkspaceFileBytes(ctx, cfg.Endpoint, workspaceID, ".room/attachments/conformance-large.bin", make([]byte, n)); err != nil {
		return fmt.Errorf("a %d-byte file write failed: a writeFile daemon accepts a file up to the chat attachment cap (%d bytes) or states a smaller limits.maxInputBytes: %w", n, protocol.MinWriteFileBytes, err)
	}
	return nil
}

func RunProvider(t *testing.T, provider core.Provider, cfg Config) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cfg))
	defer cancel()
	if err := CheckProvider(ctx, provider, cfg); err != nil {
		t.Fatal(err)
	}
}

func CheckProvider(ctx context.Context, provider core.Provider, cfg Config) error {
	if provider.Name() == "" {
		return fmt.Errorf("Name returned empty string")
	}
	workspaceID, err := checkWorkspaceLifecycle(ctx, provider, cfg)
	if err != nil {
		return err
	}
	sessionID, err := checkSessionLifecycle(ctx, provider, cfg, workspaceID)
	if err != nil {
		return err
	}
	return checkOptionalCapabilities(ctx, provider, cfg, workspaceID, sessionID)
}

func checkWorkspaceLifecycle(ctx context.Context, provider core.Provider, cfg Config) (string, error) {
	workspaceID, err := provider.CreateWorkspace(ctx, cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("CreateWorkspace: %w", err)
	}
	if workspaceID == "" {
		return "", fmt.Errorf("CreateWorkspace returned empty workspace id")
	}
	return workspaceID, nil
}

func checkSessionLifecycle(ctx context.Context, provider core.Provider, cfg Config, workspaceID string) (string, error) {
	targets, err := provider.ListTargets(ctx, cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("ListTargets: %w", err)
	}
	targetID := cfg.TargetID
	if targetID == "" && len(targets) > 0 {
		targetID = targets[0].ID
	}
	res, err := provider.CreateSession(ctx, cfg.Endpoint, core.SessionRequest{WorkspaceID: workspaceID, TargetID: targetID, Prompt: prompt(cfg)})
	if err != nil {
		return "", fmt.Errorf("CreateSession: %w", err)
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("CreateSession returned empty session id")
	}
	if err := provider.SendInput(ctx, cfg.Endpoint, workspaceID, res.SessionID, input(cfg), core.SessionOptions{}); err != nil {
		return "", fmt.Errorf("SendInput: %w", err)
	}
	if _, _, err := provider.Messages(ctx, cfg.Endpoint, workspaceID, res.SessionID, 0); err != nil {
		return "", fmt.Errorf("Messages: %w", err)
	}
	if _, err := provider.PollTurn(ctx, cfg.Endpoint, workspaceID, res.SessionID); err != nil {
		return "", fmt.Errorf("PollTurn: %w", err)
	}
	return res.SessionID, nil
}

func checkOptionalCapabilities(ctx context.Context, provider core.Provider, cfg Config, workspaceID, sessionID string) error {
	if interrupter, ok := core.As[core.TurnInterrupter](ctx, provider, cfg.Endpoint, core.CapabilityInterrupt); ok {
		if err := interrupter.Interrupt(ctx, cfg.Endpoint, workspaceID, sessionID); err != nil {
			return fmt.Errorf("Interrupt advertised but failed: %w", err)
		}
	}
	if steerer, ok := core.As[core.Steerer](ctx, provider, cfg.Endpoint, core.CapabilitySteer); ok {
		// A steer is safe to ask whatever the session is doing: it answers one of three outcomes,
		// consumes nothing unless it injects, and never starts, cancels or fails a turn.
		out, err := steerer.Steer(ctx, cfg.Endpoint, workspaceID, sessionID, "conformance steer: ignore this line")
		if err != nil {
			return fmt.Errorf("Steer advertised but failed: %w", err)
		}
		if !out.Valid() {
			return fmt.Errorf("Steer advertised but answered the unknown outcome %q", out)
		}
	}
	if writer, ok := core.As[core.WorkspaceFileWriter](ctx, provider, cfg.Endpoint, core.CapabilityWriteFile); ok {
		if err := checkWorkspaceFileWriter(ctx, writer, cfg, workspaceID); err != nil {
			return err
		}
	}
	if err := checkAgentProfile(ctx, provider, cfg, workspaceID); err != nil {
		return err
	}
	if err := checkSessionFiles(ctx, provider, cfg, workspaceID, sessionID); err != nil {
		return err
	}
	if deleter, ok := core.As[core.WorkspaceDeleter](ctx, provider, cfg.Endpoint, core.CapabilityDeleteWorkspace); ok {
		if err := deleter.DeleteWorkspace(ctx, cfg.Endpoint, workspaceID); err != nil {
			return fmt.Errorf("DeleteWorkspace advertised but failed: %w", err)
		}
	}
	if discoverer, ok := core.As[core.Discoverer](ctx, provider, cfg.Endpoint, core.CapabilityDiscover); ok {
		if ep, found := discoverer.Discover(ctx); found && ep.BaseURL == "" {
			return fmt.Errorf("Discover advertised found=true with empty BaseURL")
		}
	}
	return nil
}

// checkWorkspaceFileWriter exercises the writeFile path contract
// (core.WorkspaceFileAllowed): a .room/ file and an agent-config file are
// written; a path outside both is refused.
func checkWorkspaceFileWriter(ctx context.Context, writer core.WorkspaceFileWriter, cfg Config, workspaceID string) error {
	if err := writer.WriteWorkspaceFile(ctx, cfg.Endpoint, workspaceID, filePath(cfg), fileContent(cfg)); err != nil {
		return fmt.Errorf("WriteWorkspaceFile advertised but failed: %w", err)
	}
	if err := writer.WriteWorkspaceFile(ctx, cfg.Endpoint, workspaceID, ".agents/skills/conformance/SKILL.md", "---\nname: conformance\ndescription: conformance check\n---\n"); err != nil {
		return fmt.Errorf("WriteWorkspaceFile refused an agent-config path: %w", err)
	}
	if err := writer.WriteWorkspaceFile(ctx, cfg.Endpoint, workspaceID, "conformance-outside.txt", "x"); err == nil {
		return fmt.Errorf("WriteWorkspaceFile accepted a path outside .room/ and the agent-config paths")
	}
	return nil
}

// checkAgentProfile exercises agentProfile when offered: a session created
// with a profile (instructions, a skill, an env var) must start.
func checkAgentProfile(ctx context.Context, provider core.Provider, cfg Config, workspaceID string) error {
	if !core.Supports(ctx, provider, cfg.Endpoint, core.CapabilityAgentProfile) {
		return nil
	}
	targetID := cfg.TargetID
	if targetID == "" {
		if targets, err := provider.ListTargets(ctx, cfg.Endpoint); err == nil && len(targets) > 0 {
			targetID = targets[0].ID
		}
	}
	profile := &core.AgentProfile{
		Instructions: "Conformance profile: answer briefly.",
		Skills: []core.ProfileSkill{{Name: "conformance", Files: []core.ProfileFile{
			{Path: "SKILL.md", Content: []byte("---\nname: conformance\ndescription: Use when asked about conformance.\n---\nReply OK.\n")},
		}}},
		Env: map[string]string{"BNBLOC_CONFORMANCE": "1"},
	}
	res, err := provider.CreateSession(ctx, cfg.Endpoint, core.SessionRequest{WorkspaceID: workspaceID, TargetID: targetID, Prompt: prompt(cfg), Profile: profile})
	if err != nil {
		return fmt.Errorf("CreateSession with a profile (agentProfile advertised) failed: %w", err)
	}
	if res.SessionID == "" {
		return fmt.Errorf("CreateSession with a profile returned empty session id")
	}
	return nil
}

// checkSessionFiles exercises sessionFiles (and sessionFileBytes) when
// offered: the listing must succeed with clean workspace-relative,
// slash-separated paths; with exact reads, the first listed file must read
// back at its listed size and a path escaping the workspace must be refused.
func checkSessionFiles(ctx context.Context, provider core.Provider, cfg Config, workspaceID, sessionID string) error {
	files, ok := core.As[core.SessionFiles](ctx, provider, cfg.Endpoint, core.CapabilitySessionFiles)
	if !ok {
		return nil
	}
	listed, err := files.ListSessionFiles(ctx, cfg.Endpoint, workspaceID, sessionID)
	if err != nil {
		return fmt.Errorf("ListSessionFiles advertised but failed: %w", err)
	}
	for _, f := range listed {
		if !fs.ValidPath(f.Path) || f.Path == "." || strings.Contains(f.Path, "\\") {
			return fmt.Errorf("ListSessionFiles returned %q; want a clean workspace-relative slash-separated path", f.Path)
		}
	}
	if _, exact := core.As[core.SessionFiles](ctx, provider, cfg.Endpoint, core.CapabilitySessionFileBytes); !exact {
		return nil
	}
	if len(listed) > 0 {
		content, err := files.ReadSessionFile(ctx, cfg.Endpoint, workspaceID, sessionID, listed[0].Path)
		if err != nil {
			return fmt.Errorf("ReadSessionFile(%q) advertised but failed: %w", listed[0].Path, err)
		}
		if int64(len(content)) != listed[0].Bytes {
			return fmt.Errorf("ReadSessionFile(%q) returned %d bytes; listing says %d", listed[0].Path, len(content), listed[0].Bytes)
		}
	}
	if _, err := files.ReadSessionFile(ctx, cfg.Endpoint, workspaceID, sessionID, "../conformance-escape"); err == nil {
		return fmt.Errorf("ReadSessionFile accepted a path escaping the workspace")
	}
	return nil
}

// checkDiscoveryCapabilities enforces capability pairings the wire requires:
// reads have only the exact-bytes form, so sessionFiles and sessionFileBytes
// are advertised together.
func checkDiscoveryCapabilities(d protocol.Discovery) error {
	list := protocol.HasCapability(d, protocol.CapabilitySessionFiles)
	read := protocol.HasCapability(d, protocol.CapabilitySessionFileBytes)
	if list != read {
		return fmt.Errorf("discovery advertises %s=%v but %s=%v; a core advertises both or neither",
			protocol.CapabilitySessionFiles, list, protocol.CapabilitySessionFileBytes, read)
	}
	return nil
}

// checkLiveErrorEnvelope asks for a documented missing session through the
// protocol client: the daemon must answer with an error envelope that names
// a code and a message.
func checkLiveErrorEnvelope(ctx context.Context, provider *protocol.Client, cfg Config) error {
	_, err := provider.PollTurn(ctx, cfg.Endpoint, "__missing_workspace__", "__missing_session__")
	if err == nil {
		return fmt.Errorf("missing session answered 2xx; want a 4xx/5xx error envelope")
	}
	var pe *protocol.ProtocolError
	if !errors.As(err, &pe) {
		return fmt.Errorf("missing session error carries no error envelope: %w", err)
	}
	if pe.Code == "" || pe.Message == "" {
		return fmt.Errorf("error envelope missing code/message: %+v", pe)
	}
	return nil
}

func timeout(cfg Config) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return 30 * time.Second
}

func prompt(cfg Config) string {
	if cfg.Prompt != "" {
		return cfg.Prompt
	}
	return "Core Protocol v1 conformance prompt."
}

func input(cfg Config) string {
	if cfg.Input != "" {
		return cfg.Input
	}
	return "Core Protocol v1 conformance follow-up."
}

func filePath(cfg Config) string {
	if cfg.FilePath != "" {
		return cfg.FilePath
	}
	return ".room/conformance.txt"
}

func fileContent(cfg Config) string {
	if cfg.FileContent != "" {
		return cfg.FileContent
	}
	return "core protocol conformance\n"
}
