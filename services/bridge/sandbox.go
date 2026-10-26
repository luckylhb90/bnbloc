package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// Agent sandbox (hybrid execution, docs/prd/2026-10-09-agent-vm-sandbox.md):
// the agent CLI stays on the member's machine, confined to a room workspace;
// its shell commands run in that room's SPACE of one shared Linux machine the
// desktop prepared. The bridge never creates or deletes machines: per
// (room, core, provider) it ensures the space (the room workspace under the
// shared workspaces root, mounted at the identical guest path), mints a
// short-lived space-scoped exec token before every sandboxed turn, and hands
// the core an execution descriptor — only to a core that advertises
// sandboxExec.

// SandboxMode is the member's agent-sandbox setting (BNBLOC_SANDBOX_MODE).
type SandboxMode string

const (
	// SandboxOff: nothing runs sandboxed (the desktop runs no machine).
	SandboxOff SandboxMode = "off"
	// SandboxBorrowed (default): borrowed drives run sandboxed when possible.
	SandboxBorrowed SandboxMode = "borrowed"
	// SandboxAll: own drives too.
	SandboxAll SandboxMode = "all"
	// SandboxPending: the desktop does not know yet whether a sandbox is
	// installed or how it is set (its component catalog is not read yet).
	// Every drive a mode could sandbox waits (sandbox_starting) until the
	// desktop restarts the bridge with the mode it learned: none runs on
	// the host on a guess.
	SandboxPending SandboxMode = "pending"
)

// ParseSandboxMode reads BNBLOC_SANDBOX_MODE ("" = the default, borrowed).
func ParseSandboxMode(raw string) (SandboxMode, error) {
	switch m := SandboxMode(strings.ToLower(strings.TrimSpace(raw))); m {
	case "":
		return SandboxBorrowed, nil
	case SandboxOff, SandboxBorrowed, SandboxAll, SandboxPending:
		return m, nil
	default:
		return "", fmt.Errorf("BNBLOC_SANDBOX_MODE %q: want off, borrowed, all or pending", raw)
	}
}

// ParseSandboxEnv reads BNBLOC_SANDBOX_ENV (comma or whitespace separated):
// the patterns pkgmeta.ValidForwardEnv admits, each once, and the entries
// it refused.
func ParseSandboxEnv(raw string) (env, refused []string) {
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		switch {
		case !pkgmeta.ValidForwardEnv(p):
			refused = append(refused, p)
		case !slices.Contains(env, p):
			env = append(env, p)
		}
	}
	return env, refused
}

// SandboxConfig is the sandbox the desktop handed this bridge at launch. All
// of it lives in memory only; Token is the sandbox ADMIN bearer: it never
// leaves the bridge (cores get space-scoped exec tokens) and is never logged
// (LogValue redacts the whole config).
type SandboxConfig struct {
	Mode SandboxMode
	// Env is what the member lets into the sandboxed runs of their own
	// agents beyond the default (BNBLOC_SANDBOX_ENV, ParseSandboxEnv):
	// environment variable names or PREFIX* patterns, never values. Only
	// a drive decideSandbox marks ownerEnv gets it.
	Env []string
	// URL / Token / VMExec / Machine / WorkspacesRoot are set only while the
	// sandbox is ready and the mode is not off; any of them empty = the
	// sandbox is unavailable.
	URL            string
	Token          string
	VMExec         string
	Machine        string
	WorkspacesRoot string
	// OwnMachine is the member's own machine (BNBLOC_SANDBOX_OWN_MACHINE,
	// "" = none): the shared machine's egress plus the private hosts the
	// member allowed (docs/prd/2026-10-21-sandbox-private-hosts.md). Only a
	// drive on its owner's own behalf runs there (sandboxDecision.ownerEnv),
	// always while it is set: the desktop sets it once the machine runs and
	// withdraws it when the machine is not wanted or keeps failing, so a
	// session never moves between machines; the machine briefly missing
	// (recreated for a change of hosts) is a retried sandbox_starting.
	OwnMachine string
	// MountsDir is where components write the mounts they contribute to a
	// sandboxed turn (sandbox_mounts.go; "" = none are read), and
	// MountGuestRoots the guest folders they may mount under.
	MountsDir       string
	MountGuestRoots []string
	// Account names who this bridge serves (its gateway and owner,
	// BridgeAccount): it is part of every space's name, so two accounts on one
	// computer, with agents of one core and provider in one room, never share
	// a space or its workspace folder. Empty keeps the names an agent had
	// before (a bridge that serves no account of a desktop's).
	Account string
	// HostProxy is where the guests reach this bridge's egress proxy
	// (BNBLOC_SANDBOX_HOST_PROXY, "<host alias>:<port>": the sandbox's
	// hostProxy, the machine opening only that host loopback port); ""
	// gives sandboxed commands no egress proxy.
	HostProxy string
	// PrivateHosts are the hosts the member opened for their own machine
	// (BNBLOC_SANDBOX_PRIVATE_HOSTS): sandboxed commands reach them through
	// the egress proxy like any host (a connector's host among them is
	// served by its service), whose tunnels reach a private address only
	// for these.
	PrivateHosts []string
}

// hostProxyPort is HostProxy's port ("" when it has none).
func (c SandboxConfig) hostProxyPort() string {
	_, port, err := net.SplitHostPort(c.HostProxy)
	if err != nil {
		return ""
	}
	return port
}

// BridgeAccount is SandboxConfig.Account for the bridge that serves owner on
// the gateway: "" when either is not known.
func BridgeAccount(gatewayBaseURL, owner string) string {
	gateway, owner := strings.TrimRight(strings.TrimSpace(gatewayBaseURL), "/"), strings.TrimSpace(owner)
	if gateway == "" || owner == "" {
		return ""
	}
	return gateway + "\n" + owner
}

// LogValue keeps the config (and its bearer) out of every log line.
func (c SandboxConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("mode", string(c.Mode)), slog.Bool("present", c.present()))
}

func (c SandboxConfig) present() bool {
	return c.URL != "" && c.Token != "" && c.VMExec != "" && c.Machine != "" && c.WorkspacesRoot != ""
}

const (
	// sandboxExecTokenTTL bounds a sandboxed turn's command access: a token
	// is minted per turn and, while the turn runs on a core that takes a
	// fresh execution (executionRefresh), renewed every sandboxExecRenewEvery
	// (renewExecution), so a turn may run for hours while each token stays
	// short-lived and lapses within the hour after the turn ends.
	sandboxExecTokenTTL = time.Hour
	// sandboxExecRenewEvery is how often a running turn's token is renewed.
	sandboxExecRenewEvery = 30 * time.Minute
	// sandboxExecTokenNoRefreshTTL is a turn's token on a core that cannot
	// take a fresh execution: it cannot be renewed, so it outlives longer
	// turns instead.
	sandboxExecTokenNoRefreshTTL = 4 * time.Hour
	// sandboxReadyTTL / sandboxNotReadyTTL cache the readiness probe.
	sandboxReadyTTL    = 30 * time.Second
	sandboxNotReadyTTL = 10 * time.Second
	// sandboxPolicyTTL caches a tenant's requireSandboxForBorrow.
	sandboxPolicyTTL = 30 * time.Second
)

// sandboxRequiredCaps are what bnbloc's shared-machine mode needs.
var sandboxRequiredCaps = []string{sandbox.CapabilitySpaces, sandbox.CapabilityShares, sandbox.CapabilityExecTokens}

// sandboxRuntime is the bridge's view of the desktop's sandbox.
type sandboxRuntime struct {
	cfg    SandboxConfig
	client *sandbox.Client
	now    func() time.Time
	// spaceEnv, when set, is the default environment of a space for its
	// workdir: the egress proxy (roomtools_contrib.go).
	spaceEnv func(ctx context.Context, workdir string) map[string]string

	mu        sync.Mutex
	ready     bool
	reason    string // why it is not ready (member-readable)
	checkedAt time.Time
	// quiesced holds the spaces the bridge stopped every process of since
	// their last use (the turn fence: sandbox_fence.go).
	quiesced map[string]bool
	// readOnlyMounts is whether the probed sandbox advertises
	// readOnlyMounts: only then are a component's mounts offered to a guest.
	readOnlyMounts bool

	policyMu sync.Mutex
	policy   map[string]sandboxPolicyEntry // tenant -> cached policy
}

type sandboxPolicyEntry struct {
	require bool
	at      time.Time
}

func newSandboxRuntime(cfg SandboxConfig) *sandboxRuntime {
	c := sandbox.NewClient(nil)
	c.REST.Timeout = 30 * time.Second
	return &sandboxRuntime{cfg: cfg, client: c, now: time.Now, policy: map[string]sandboxPolicyEntry{}, quiesced: map[string]bool{}}
}

func (s *sandboxRuntime) endpoint() corepkg.Endpoint {
	return corepkg.Endpoint{BaseURL: s.cfg.URL, Token: s.cfg.Token}
}

// status reports whether the sandbox can take a drive now, probing at most
// once per cache window: discovery (with the capabilities shared-machine mode
// needs) and the desktop's machine running.
func (s *sandboxRuntime) status(ctx context.Context) (bool, string) {
	if s.cfg.Mode == SandboxOff {
		return false, "the agent sandbox is off"
	}
	if !s.cfg.present() {
		return false, "the agent sandbox is not running"
	}
	s.mu.Lock()
	ttl := sandboxNotReadyTTL
	if s.ready {
		ttl = sandboxReadyTTL
	}
	if !s.checkedAt.IsZero() && s.now().Sub(s.checkedAt) < ttl {
		ready, reason := s.ready, s.reason
		s.mu.Unlock()
		return ready, reason
	}
	s.mu.Unlock()
	ready, reason := s.probe(ctx)
	s.mu.Lock()
	changed := ready != s.ready || s.checkedAt.IsZero()
	s.ready, s.reason, s.checkedAt = ready, reason, s.now()
	s.mu.Unlock()
	if changed {
		if ready {
			slog.Info("bnbloc bridge agent sandbox ready", "event", "bnbloc.bridge.sandbox_ready", "machine", s.cfg.Machine, "mode", string(s.cfg.Mode))
		} else {
			slog.Warn("bnbloc bridge agent sandbox not ready", "event", "bnbloc.bridge.sandbox_not_ready", "reason", reason)
		}
	}
	return ready, reason
}

// cachedReady is the last probed readiness, without probing (heartbeat and
// roster advertisement read it every tick).
func (s *sandboxRuntime) cachedReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready && s.cfg.Mode != SandboxOff && s.cfg.present()
}

func (s *sandboxRuntime) probe(ctx context.Context) (bool, string) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, err := s.client.Discover(pctx, s.endpoint())
	if err != nil {
		return false, "the agent sandbox does not answer"
	}
	for _, c := range sandboxRequiredCaps {
		if !sandbox.HasCapability(d, c) {
			return false, "the agent sandbox lacks " + c
		}
	}
	s.mu.Lock()
	s.readOnlyMounts = sandbox.HasCapability(d, sandbox.CapabilityReadOnlyMounts)
	s.mu.Unlock()
	m, err := s.client.GetMachine(pctx, s.endpoint(), s.cfg.Machine)
	if err != nil {
		return false, "the agent sandbox machine is missing"
	}
	if m.State != sandbox.StateRunning {
		return false, "the agent sandbox machine is " + string(m.State)
	}
	return true, ""
}

// machineOf is the machine a sandboxed slot runs on.
func (s *sandboxRuntime) machineOf(k driveKey) string {
	if k.ownMachine && s.cfg.OwnMachine != "" {
		return s.cfg.OwnMachine
	}
	return s.cfg.Machine
}

// sandboxSpaceName names the space (and the workspace directory) of one
// seat of one (room, core, provider) (driveKey): readable, path- and
// user-name-safe (the default guest user is "sp-<space>", so it stays well
// under 32 characters), and collision-free through the hash of the full
// key and the account the bridge serves. The owner's own seat is a space of
// its own per account; another requester's seat is one too.
func sandboxSpaceName(k driveKey, account string) string {
	id := k.seatAgentID()
	if k.ownMachine {
		// A workspace of its own: both machines share the workspaces root.
		id += "\x00own-machine"
	}
	if account != "" {
		id += "\x00account\x00" + account
	}
	sum := sha256.Sum256([]byte(id))
	return slugPart(k.core, 8) + "-" + slugPart(k.provider, 8) + "-" + hex.EncodeToString(sum[:5])
}

func slugPart(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() >= max {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}

// prepare readies one sandboxed turn: the room workspace directory, a space
// with no process left from before (the turn fence, sandbox_fence.go), the
// space (re-ensured every turn: the runtime hands host-created entries to
// the space user only on ensure), and a fresh space-scoped exec token. The
// returned descriptor carries no admin credential. env is the owner's
// forwarded names for this drive (nil unless decideSandbox marked it
// ownerEnv): the space's envAllowlist is re-ensured from it every turn, so
// a turn for someone else after the owner's converges back to the default.
func (s *sandboxRuntime) prepare(ctx context.Context, k driveKey, owner string, env []string, ttl time.Duration, borrowed bool, denyRead []string) (*corepkg.Execution, error) {
	room, core, provider := k.room, k.core, k.provider
	space, machine := sandboxSpaceName(k, s.cfg.Account), s.machineOf(k)
	workdir := filepath.Join(s.cfg.WorkspacesRoot, space)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return nil, fmt.Errorf("agent sandbox workspace: %w", err)
	}
	if !s.takeQuiesced(space) {
		// Not quiesced by this bridge since its last use (a bridge restart,
		// a failed quiesce): a process left there could still write the
		// workdir, so it is stopped first, or the drive does not run.
		if err := s.quiesce(ctx, machine, space); err != nil {
			s.invalidate()
			return nil, fmt.Errorf("agent sandbox space: stop its processes: %w", err)
		}
		s.takeQuiesced(space)
	}
	// The workspace is mounted at the identical guest path; the core's path
	// mounts are derived from the space's.
	mounts := []sandbox.SpaceMount{{Kind: sandbox.MountShared, HostPath: workdir, GuestPath: workdir}}
	// What components contribute (read-only copies, at paths the member
	// allows) is the guest's alone: the core's path mapping is the workspace.
	space0 := len(mounts)
	contributed, mountEnv := s.contributedMounts(workdir, space, borrowed)
	mounts = append(mounts, contributed...)
	// Every drive's commands reach the services' hosts through the
	// bridge's egress proxy. Each service decides by the attested drive: a
	// borrowed or platform drive is never served in the owner's name (the
	// connections component serves one only as a digital worker).
	var spaceEnv map[string]string
	if s.spaceEnv != nil {
		spaceEnv = s.spaceEnv(ctx, workdir)
	}
	// A per-space mount names its guest path in a variable of the space.
	if len(mountEnv) > 0 {
		merged := make(map[string]string, len(spaceEnv)+len(mountEnv))
		for k, v := range spaceEnv {
			merged[k] = v
		}
		for k, v := range mountEnv {
			merged[k] = v
		}
		spaceEnv = merged
	}
	ensure := func(mounts []sandbox.SpaceMount) error {
		_, err := s.client.EnsureSpace(ctx, s.endpoint(), machine, space, sandbox.SpaceRequest{
			Workdir:      workdir,
			Mounts:       mounts,
			Env:          spaceEnv,
			EnvAllowlist: coreprotocol.ForwardEnvAllowlist(env),
			Labels:       sandbox.Labels{Room: room, Core: core, Provider: provider, Owner: owner},
		})
		return err
	}
	err := ensure(mounts)
	if sandbox.ErrorCodeOf(err) == sandbox.ErrorConflict && len(mounts) > space0 {
		// A guest path belongs to one space of the machine: a component's mount at
		// a fixed path is the first room's, and this room is refused it. The turn
		// never fails because of a component, so it runs without the mounts that
		// cannot be shared (the per-space ones, which can, stay).
		kept := mounts[:space0:space0]
		for _, m := range mounts[space0:] {
			if isPerSpaceTree(m.HostPath) {
				kept = append(kept, m)
			}
		}
		slog.Warn("bnbloc bridge sandbox mounts at a fixed guest path dropped: another room's space holds the path", "event", "bnbloc.bridge.sandbox_mounts_conflict", "dropped", len(mounts)-len(kept))
		mounts, err = kept, ensure(kept)
	}
	if err != nil {
		s.invalidate()
		return nil, fmt.Errorf("agent sandbox space: %w", err)
	}
	tok, err := s.client.MintExecToken(ctx, s.endpoint(), machine, space, int64(ttl/time.Second))
	if err != nil {
		s.invalidate()
		return nil, fmt.Errorf("agent sandbox exec token: %w", err)
	}
	return &corepkg.Execution{Sandbox: corepkg.SandboxExecution{
		URL:         s.cfg.URL,
		ExecToken:   tok.Token,
		ExpiresAtMs: tok.ExpiresAtMs,
		Machine:     machine,
		Space:       space,
		VMExec:      s.cfg.VMExec,
		Workdir:     workdir,
		Mounts:      pathMounts(mounts[:space0]),
		Env:         env,
		// The protected paths (the role the session wears, the room
		// context, host-executed project config) are write-denied: a
		// borrowed prompt can neither rewrite its role nor plant config a
		// harness would execute on the host. Guest commands write the
		// shared workdir directly; the turn fence covers those.
		HostConfine: corepkg.HostConfine{Workspace: workdir, DenyWrite: protectedDenyWrite(workdir), DenyRead: denyRead},
	}}, nil
}

// renewed is exec with a freshly minted token for its space (a running
// turn's renewal; ttl as prepare's).
func (s *sandboxRuntime) renewed(ctx context.Context, exec *corepkg.Execution, ttl time.Duration) (*corepkg.Execution, error) {
	tok, err := s.client.MintExecToken(ctx, s.endpoint(), exec.Sandbox.Machine, exec.Sandbox.Space, int64(ttl/time.Second))
	if err != nil {
		return nil, fmt.Errorf("agent sandbox exec token: %w", err)
	}
	next := *exec
	next.Sandbox.ExecToken, next.Sandbox.ExpiresAtMs = tok.Token, tok.ExpiresAtMs
	return &next, nil
}

// pathMounts is a core's host→guest path mapping of a space's shared mounts.
func pathMounts(mounts []sandbox.SpaceMount) []corepkg.PathMount {
	out := make([]corepkg.PathMount, 0, len(mounts))
	for _, m := range mounts {
		if m.Kind == sandbox.MountShared {
			out = append(out, corepkg.PathMount{Host: m.HostPath, Guest: m.GuestPath})
		}
	}
	return out
}

// invalidate forces the next status call to probe again (a failed ensure or
// mint usually means the sidecar or machine went away).
func (s *sandboxRuntime) invalidate() {
	s.mu.Lock()
	s.checkedAt = time.Time{}
	s.mu.Unlock()
}

// sandboxDecision is how one drive runs.
type sandboxDecision struct {
	sandboxed bool
	// wait: the owner's mode puts this drive in the sandbox and the
	// sandbox is not ready yet; the drive fails sandbox_starting (retried)
	// instead of running anywhere else meanwhile.
	wait bool
	// ownerEnv: the sandboxed drive runs on its owner's own behalf, wearing
	// nothing from someone else that runs scripts, so it gets the owner's
	// forwarded environment (SandboxConfig.Env). Never a borrowed or
	// platform drive, and never one forced into the sandbox by foreign
	// content: the owner's secrets must not reach someone else's prompt or
	// code.
	ownerEnv bool
	// refuse is the member-readable reason a borrowed drive is refused
	// (tenant policy requires the sandbox); empty = run.
	refuse string
	// unavailable says why a borrowed drive is not sandboxed (the tail of a
	// refusal: "<owner>'s agent sandbox is off. …"), for a requirement that
	// only a later answer reveals — a platform agent's reservation
	// (sandboxRequired). Empty when the drive is sandboxed.
	unavailable string
}

// Refusal prefixes; each ends where sandboxUnavailable's tail begins.
const (
	tenantSandboxPrefix   = "Your organization requires borrowed agents to run in their owner's agent sandbox, and "
	platformSandboxPrefix = "Platform agents always run in their host's agent sandbox, and "
	// policyUnreadablePrefix: the tenant's requirement could not be read, so
	// the borrowed drive is held to it (fail closed).
	policyUnreadablePrefix = "Your organization's sandbox policy for borrowed agents could not be checked right now, so this borrowed run must use its owner's agent sandbox, and "
)

// contentSandboxPrefix is the refusal of a turn wearing someone else's
// content that runs scripts or a stdio tool (names listed).
func contentSandboxPrefix(foreign []string) string {
	return "This turn uses " + strings.Join(foreign, ", ") + " from someone else, which runs scripts or tools and so runs only in the agent sandbox, and "
}

// requiresSandboxPrefix is the refusal of a run of an agent that exists only in
// its owner's sandbox, when the sandbox cannot take it: such an agent has no
// host install to fall back to.
const requiresSandboxPrefix = "This agent is installed only in its owner's agent sandbox and runs only there, and "

// ownerOnlyRefusal is the refusal of a borrow (or a platform seat) of such an
// agent: the credentials it signs in with exist only for its owner's own runs,
// so another person's run could not work, and giving it the owner's would hand
// them over.
const ownerOnlyRefusal = "This agent is installed only in its owner's agent sandbox, with a sign-in only the owner has, so it cannot be borrowed or used by anyone else."

// decideSandbox applies the PRD policy. Own drives are sandboxed only in mode
// all; borrowed drives in mode borrowed or all, when the core supports
// sandboxExec. A drive the mode puts in the sandbox waits for it while it is
// not ready (wait: sandbox_starting, retried): it never runs on the host
// meanwhile, where the member's choice would be dropped without a word
// (docs/bugs/2026-10-21-sandboxed-sessions-reset-every-turn.md). A borrowed
// drive the mode or the core leaves out runs on the host as before — unless
// the tenant requires the sandbox for borrowing, or its policy cannot be
// read, then it is refused (fail closed, visible notice). requirePolicy answers the tenant's switch
// and an error when it is unreadable. A drive wearing someone else's
// script-bearing content (foreign, the content's names) is sandboxed like a
// borrowed one, and refused when it cannot be: such content never runs on
// the owner's host. Only a sandboxed drive that is neither borrowed nor
// forced gets the owner's forwarded environment (ownerEnv).
func decideSandbox(borrowed bool, foreign []string, requires bool, mode SandboxMode, ready bool, notReadyReason string, coreSupports bool, requirePolicy func() (bool, error), owner, provider string) sandboxDecision {
	// requires: the target exists only in the sandbox. It is forced like foreign
	// content (sandboxed, or refused, never the host), but unlike foreign content
	// it is the owner's own run, so the owner's own environment still applies.
	forcedForeign := len(foreign) > 0
	forced := forcedForeign || requires
	if coreSupports && (mode == SandboxAll || mode == SandboxPending || ((borrowed || forced) && mode == SandboxBorrowed)) {
		if !ready {
			return sandboxDecision{wait: true}
		}
		return sandboxDecision{sandboxed: true, ownerEnv: !borrowed && !forcedForeign}
	}
	if forced {
		unavailable := sandboxUnavailable(mode, ready, notReadyReason, coreSupports, owner, provider)
		prefix := requiresSandboxPrefix
		if forcedForeign {
			prefix = contentSandboxPrefix(foreign)
		}
		return sandboxDecision{refuse: prefix + unavailable, unavailable: unavailable}
	}
	if !borrowed {
		return sandboxDecision{}
	}
	unavailable := sandboxUnavailable(mode, ready, notReadyReason, coreSupports, owner, provider)
	require, err := requirePolicy()
	switch {
	case err != nil:
		return sandboxDecision{refuse: policyUnreadablePrefix + unavailable, unavailable: unavailable}
	case require:
		return sandboxDecision{refuse: tenantSandboxPrefix + unavailable, unavailable: unavailable}
	}
	return sandboxDecision{unavailable: unavailable}
}

// sandboxUnavailable explains why a borrowed drive cannot run sandboxed.
func sandboxUnavailable(mode SandboxMode, ready bool, notReadyReason string, coreSupports bool, owner, provider string) string {
	switch {
	case mode == SandboxOff:
		return owner + "'s agent sandbox is off. " + owner + " can turn it on in Components → the sandbox → Settings."
	case !ready:
		reason := notReadyReason
		if reason == "" {
			reason = "the agent sandbox is not ready"
		}
		return "on " + owner + "'s machine " + reason + "."
	case !coreSupports:
		return owner + "'s " + provider + " cannot run sandboxed (its core does not support sandbox execution)."
	default:
		return "on " + owner + "'s machine the agent sandbox is not ready."
	}
}

// requireSandboxForBorrow reads (and caches for sandboxPolicyTTL) the
// tenant's sandbox switch from the gateway's tenant policy: GET
// /v1/my/tenant-policy?tenant=. It is consulted only for a borrowed drive
// that cannot run sandboxed; a read that fails returns the error and the
// drive is refused (fail closed): running it on the host would bypass a
// requirement nobody could check.
func (b *Bridge) requireSandboxForBorrow(ctx context.Context, tenantID string) (bool, error) {
	s := b.sandbox
	key := strings.TrimSpace(tenantID)
	s.policyMu.Lock()
	prev, known := s.policy[key]
	s.policyMu.Unlock()
	if known && s.now().Sub(prev.at) < sandboxPolicyTTL {
		return prev.require, nil
	}
	require, err := b.fetchSandboxPolicy(ctx, key)
	if err != nil {
		slog.Warn("bnbloc bridge: sandbox policy unreadable; refusing the unsandboxed borrowed drive",
			"event", "bnbloc.sandbox.policy_unreadable", "tenant", key, "error", err)
		return false, err
	}
	s.policyMu.Lock()
	s.policy[key] = sandboxPolicyEntry{require: require, at: s.now()}
	s.policyMu.Unlock()
	return require, nil
}

func (b *Bridge) fetchSandboxPolicy(ctx context.Context, tenantKey string) (bool, error) {
	path := "/v1/my/tenant-policy"
	if tenantKey != "" {
		path += "?tenant=" + url.QueryEscape(tenantKey)
	}
	raw, err := b.gatewayGET(ctx, path)
	if err != nil {
		return false, err
	}
	var out struct {
		RequireSandboxForBorrow *bool `json:"requireSandboxForBorrow"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.RequireSandboxForBorrow == nil {
		return false, errors.New("malformed tenant policy")
	}
	return *out.RequireSandboxForBorrow, nil
}

// planSandbox decides how a drive runs (stage 4) and fills it in:
// d.prepareExec readies a sandboxed turn (nil: the host), d.unsandboxed
// says why a borrowed drive is not sandboxed (sandboxDecision.unavailable)
// and d.ownSandbox whether the sandboxed drive gets its owner's forwarded
// environment (sandboxDecision.ownerEnv). It returns the refusal text when
// the drive must not run at all. Whether the core can run sandboxed is the
// drive's capability snapshot.
func (b *Bridge) planSandbox(ctx context.Context, d *drive) (refuse string) {
	s := b.sandbox
	requires := b.targetRequiresSandbox(d.core, d.target, d.job.Provider)
	if requires && d.borrowed {
		// A borrow and a platform seat are both someone else's run.
		return ownerOnlyRefusal
	}
	ready, reason := s.status(ctx)
	dec := decideSandbox(d.borrowed, d.content.foreign, requires, s.cfg.Mode, ready, reason, d.caps.workdir != nil, func() (bool, error) {
		return b.requireSandboxForBorrow(ctx, d.job.TenantID)
	}, d.owner, d.job.Provider)
	d.unsandboxed = dec.unavailable
	if dec.wait {
		// Failed as a machine still starting is (sandbox_starting, retried),
		// before anything is reserved or reaches a core.
		d.notReady = sandboxNotReadyError{err: errors.New(firstNonEmpty(reason, "the agent sandbox is not ready"))}
		return ""
	}
	if dec.refuse != "" || !dec.sandboxed {
		return dec.refuse
	}
	var env []string
	if dec.ownerEnv {
		env = s.cfg.Env
	}
	d.ownSandbox = dec.ownerEnv
	// Only a drive on its owner's own behalf reaches the private hosts the
	// owner allowed: a borrowed, platform or foreign-content drive never
	// runs on the own machine.
	d.ownMachine = dec.ownerEnv && s.cfg.OwnMachine != ""
	owner := d.owner
	key := driveKey{room: d.job.RoomID, core: d.core, provider: d.job.Provider, sandboxed: true, ownMachine: d.ownMachine, owner: d.seatOwner, requester: d.seatRequester()}
	d.prepareExec = func(ctx context.Context) (*corepkg.Execution, error) {
		// What the host-run tools keep private is read-denied to the confined
		// agent (docs/prd/2026-10-22-host-run-mcp-tools.md §2.4); when it
		// cannot be named the drive does not run confined without it.
		denyRead, err := d.content.hostPrivateDirs()
		if err != nil {
			return nil, err
		}
		exec, err := s.prepare(ctx, key, owner, env, d.execTokenTTL(), d.borrowed, denyRead)
		if err != nil {
			return nil, s.prepareFailure(ctx, err)
		}
		return exec, nil
	}
	return ""
}

// prepareFailure is a failed prepare as the drive reports it: a
// sandboxNotReadyError (job failure sandbox_starting, retried) when the
// sandbox is not up yet, judged by the error and a fresh readiness probe
// (a failed ensure or mint invalidated the cached one), else err.
func (s *sandboxRuntime) prepareFailure(ctx context.Context, err error) error {
	ready, _ := s.status(ctx)
	if sandboxStarting(err, ready) {
		return sandboxNotReadyError{err: err}
	}
	return err
}

// sandboxedAgent reports whether this bridge runs the borrowed drives of a
// (core, provider) sandboxed right now — the roster's shield badge. It uses
// the cached readiness and discovery (no probe of its own).
func (b *Bridge) sandboxedAgent(ctx context.Context, coreName string) bool {
	if b.sandbox == nil || b.sandbox.cfg.Mode == SandboxOff || !b.sandbox.cachedReady() {
		return false
	}
	entry, ok := b.coreEntry(coreName)
	return ok && entry.endpoint.OK() && corepkg.Supports(ctx, entry.provider, entry.endpoint, corepkg.CapabilitySandboxExec)
}
