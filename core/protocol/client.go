package protocol

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core"
)

// discoveryTTL bounds how long a daemon's discovery document (or its version
// incompatibility) is trusted. A core can be upgraded or restarted in place
// with a different capability set; caching forever would pin the old answer
// until the bridge/gateway restarted.
const discoveryTTL = time.Minute

// Client drives one Core Protocol v1 daemon. It implements core.Provider plus
// every optional capability; which optional capabilities are actually usable
// is answered per endpoint by Supports from the daemon's (cached) discovery,
// so callers probe through core.As. Construction needs no network: discovery
// is lazy, so a core whose daemon starts after bnbloc is picked up on first
// use instead of being dropped at boot.
type Client struct {
	REST core.REST

	mu   sync.Mutex
	disc map[string]discoveryEntry
	now  func() time.Time
}

type discoveryEntry struct {
	d   Discovery
	err error // a version incompatibility (transport errors are never cached)
	at  time.Time
}

func NewClient(name string, httpClient *http.Client) *Client {
	return &Client{REST: core.REST{Name: name, HTTP: httpClient, DecodeError: core.DecodeEnvelope[ProtocolError]()}}
}

func (c *Client) Name() string {
	if c.REST.Name != "" {
		return c.REST.Name
	}
	return "protocol"
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Discovery returns the daemon's discovery document, validating protocol
// compatibility. Results (and incompatibilities) are cached per endpoint for
// discoveryTTL; transport failures are not cached, so a daemon that comes up
// later is discovered on the next call.
func (c *Client) Discovery(ctx context.Context, ep core.Endpoint) (Discovery, error) {
	key := ep.BaseURL + "\x00" + ep.Token
	c.mu.Lock()
	if e, ok := c.disc[key]; ok && c.clock().Sub(e.at) < discoveryTTL {
		c.mu.Unlock()
		return e.d, e.err
	}
	c.mu.Unlock()

	var d Discovery
	if err := c.getJSON(ctx, ep, PathDiscover, &d); err != nil {
		return Discovery{}, fmt.Errorf("discover core protocol: %w", err)
	}
	entry := discoveryEntry{d: d, at: c.clock()}
	if err := Compatible(d.Protocol); err != nil {
		entry = discoveryEntry{err: err, at: entry.at}
	}
	c.mu.Lock()
	if c.disc == nil {
		c.disc = map[string]discoveryEntry{}
	}
	c.disc[key] = entry
	c.mu.Unlock()
	return entry.d, entry.err
}

// Supports implements core.CapabilityReporter from the daemon's discovery.
// An unreachable or incompatible daemon supports nothing optional.
func (c *Client) Supports(ctx context.Context, ep core.Endpoint, capability core.Capability) bool {
	d, err := c.Discovery(ctx, ep)
	return err == nil && HasCapability(d, string(capability))
}

// CapabilitiesKnown is the discovery read Supports answers from: an
// unreachable or incompatible daemon's capabilities are unknown, not absent
// (core.CapabilitiesKnown).
func (c *Client) CapabilitiesKnown(ctx context.Context, ep core.Endpoint) error {
	_, err := c.Discovery(ctx, ep)
	return err
}

func (c *Client) CreateWorkspace(ctx context.Context, ep core.Endpoint) (string, error) {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return "", err
	}
	var out CreateWorkspaceResponse
	if err := c.postJSON(ctx, ep, PathWorkspaces, CreateWorkspaceRequest{}, &out); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return out.WorkspaceID, nil
}

// CreateWorkspaceAt implements core.WorkdirWorkspaceCreator (capability
// sandboxExec): a workspace rooted at the given host workdir.
func (c *Client) CreateWorkspaceAt(ctx context.Context, ep core.Endpoint, workdir string) (string, error) {
	if err := c.requireCapability(ctx, ep, CapabilitySandboxExec); err != nil {
		return "", err
	}
	var out CreateWorkspaceResponse
	if err := c.postJSON(ctx, ep, PathWorkspaces, CreateWorkspaceRequest{Workdir: workdir}, &out); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return out.WorkspaceID, nil
}

func (c *Client) CreateSession(ctx context.Context, ep core.Endpoint, req core.SessionRequest) (core.SessionResult, error) {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return core.SessionResult{}, err
	}
	if err := c.requireRequestCapabilities(ctx, ep, req.Profile, req.Options.Execution); err != nil {
		return core.SessionResult{}, err
	}
	var out CreateSessionResponse
	if err := c.postJSON(ctx, ep, WorkspaceSessionsPath(req.WorkspaceID), WireCreateSession(req), &out); err != nil {
		return core.SessionResult{}, fmt.Errorf("create session: %w", err)
	}
	return out.Core(), nil
}

func (c *Client) SendInput(ctx context.Context, ep core.Endpoint, workspaceID, sessionID, text string, opts core.SessionOptions) error {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return err
	}
	if err := c.requireRequestCapabilities(ctx, ep, nil, opts.Execution); err != nil {
		return err
	}
	body := SendInputRequest{Text: text, Options: WireOptions(opts), Execution: WireExecution(opts.Execution)}
	if err := c.postJSON(ctx, ep, SessionInputPath(workspaceID, sessionID), body, nil); err != nil {
		return fmt.Errorf("send input: %w", err)
	}
	return nil
}

func (c *Client) ListTargets(ctx context.Context, ep core.Endpoint) ([]core.Target, error) {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return nil, err
	}
	var out ListTargetsResponse
	if err := c.getJSON(ctx, ep, PathTargets, &out); err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	return out.Core(), nil
}

func (c *Client) Messages(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string, after int64) ([]core.Message, int64, error) {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return nil, 0, err
	}
	path := SessionMessagesPath(workspaceID, sessionID) + "?after=" + strconv.FormatInt(after, 10)
	var out MessagesResponse
	if err := c.getJSON(ctx, ep, path, &out); err != nil {
		return nil, 0, fmt.Errorf("messages: %w", err)
	}
	return out.Core(), out.LatestVersion, nil
}

func (c *Client) PollTurn(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) (core.TurnStatus, error) {
	if _, err := c.Discovery(ctx, ep); err != nil {
		return core.TurnStatus{}, err
	}
	var out PollTurnResponse
	if err := c.getJSON(ctx, ep, SessionTurnPath(workspaceID, sessionID), &out); err != nil {
		return core.TurnStatus{}, fmt.Errorf("poll turn: %w", err)
	}
	return out.Core(), nil
}

// Interrupt implements core.TurnInterrupter (usable when Supports reports
// CapabilityInterrupt; a call without it returns capability_missing).
func (c *Client) Interrupt(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) error {
	if err := c.requireCapability(ctx, ep, CapabilityInterrupt); err != nil {
		return err
	}
	if err := c.postJSON(ctx, ep, SessionInterruptPath(workspaceID, sessionID), struct{}{}, nil); err != nil {
		return fmt.Errorf("interrupt turn: %w", err)
	}
	return nil
}

// WriteWorkspaceFile implements core.WorkspaceFileWriter (see Supports). A
// path outside core.WorkspaceFileAllowed is refused before any request.
func (c *Client) WriteWorkspaceFile(ctx context.Context, ep core.Endpoint, workspaceID, relPath, content string) error {
	if err := core.CheckWorkspaceFile(relPath); err != nil {
		return err
	}
	if err := c.requireCapability(ctx, ep, CapabilityWriteFile); err != nil {
		return err
	}
	body := WriteWorkspaceFileRequest{Content: content}
	if err := c.REST.JSON(ctx, http.MethodPut, ep, WorkspaceFilePath(workspaceID, relPath), body, nil); err != nil {
		return fmt.Errorf("write workspace file: %w", err)
	}
	return nil
}

// WriteWorkspaceFileBytes implements core.WorkspaceFileBytesWriter (see Supports).
func (c *Client) WriteWorkspaceFileBytes(ctx context.Context, ep core.Endpoint, workspaceID, relPath string, content []byte) error {
	if err := core.CheckWorkspaceFile(relPath); err != nil {
		return err
	}
	if err := c.requireCapability(ctx, ep, CapabilityWriteFile); err != nil {
		return err
	}
	body := WriteWorkspaceFileRequest{ContentBase64: base64.StdEncoding.EncodeToString(content)}
	if err := c.REST.JSON(ctx, http.MethodPut, ep, WorkspaceFilePath(workspaceID, relPath), body, nil); err != nil {
		return fmt.Errorf("write workspace file bytes: %w", err)
	}
	return nil
}

// ListSessionFiles implements core.SessionFiles (capability sessionFiles):
// the session workspace's files, already filtered by the core's ignore rules
// and capped (a truncated listing is simply shorter).
func (c *Client) ListSessionFiles(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) ([]core.SessionFile, error) {
	if err := c.requireCapability(ctx, ep, CapabilitySessionFiles); err != nil {
		return nil, err
	}
	var out ListSessionFilesResponse
	if err := c.getJSON(ctx, ep, SessionFilesPath(workspaceID, sessionID), &out); err != nil {
		return nil, fmt.Errorf("list session files: %w", err)
	}
	return out.Core(), nil
}

// ReadSessionFile implements core.SessionFiles: the file's exact raw bytes
// (capability sessionFileBytes, the protocol's only read form). The session
// id is not part of the route: a file is addressed within its workspace.
func (c *Client) ReadSessionFile(ctx context.Context, ep core.Endpoint, workspaceID, _ /* sessionID */, relPath string) ([]byte, error) {
	if err := c.requireCapability(ctx, ep, CapabilitySessionFileBytes); err != nil {
		return nil, err
	}
	raw, err := c.REST.Do(ctx, http.MethodGet, ep, WorkspaceFilePath(workspaceID, relPath), nil)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	if raw == nil {
		raw = []byte{}
	}
	return raw, nil
}

// DeleteWorkspace implements core.WorkspaceDeleter (see Supports).
func (c *Client) DeleteWorkspace(ctx context.Context, ep core.Endpoint, workspaceID string) error {
	if err := c.requireCapability(ctx, ep, CapabilityDeleteWorkspace); err != nil {
		return err
	}
	if err := c.REST.JSON(ctx, http.MethodDelete, ep, WorkspacePath(workspaceID), nil, nil); err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	return nil
}

func (c *Client) requireCapability(ctx context.Context, ep core.Endpoint, cap string) error {
	d, err := c.Discovery(ctx, ep)
	if err != nil {
		return err
	}
	if HasCapability(d, cap) {
		return nil
	}
	return &core.CapabilityMissingError{Core: c.Name(), Capability: core.Capability(cap)}
}

// requireRequestCapabilities refuses a request carrying what the core does
// not advertise: a profile without agentProfile (it would silently drop the
// role the session wears) or an execution without sandboxExec (the exec
// token would go to a core that runs on the host).
func (c *Client) requireRequestCapabilities(ctx context.Context, ep core.Endpoint, profile *core.AgentProfile, exec *core.Execution) error {
	if profile != nil {
		if err := c.requireCapability(ctx, ep, CapabilityAgentProfile); err != nil {
			return err
		}
	}
	if exec != nil {
		return c.requireCapability(ctx, ep, CapabilitySandboxExec)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, ep core.Endpoint, path string, out any) error {
	return c.REST.JSON(ctx, http.MethodGet, ep, path, nil, out)
}

func (c *Client) postJSON(ctx context.Context, ep core.Endpoint, path string, body any, out any) error {
	return c.REST.JSON(ctx, http.MethodPost, ep, path, body, out)
}
