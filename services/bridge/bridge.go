// Package bridge connects a member's local agent cores to the bnbloc
// control-plane without changing those cores or the desktop app.
//
// The bnbloc collaboration loop (chat drives an agent; the agent's replies appear
// in the room) runs through each configured core's public HTTP API:
//
//  1. Advertise the local agent roster and maintain room liveness with gateway
//     subscriptions (outbound; NAT-friendly).
//  2. Poll the gateway's durable job queue for agent-directed room messages.
//  3. Drive the caller's local core: create a workspace + agent session on
//     first job, then send follow-up input.
//  4. Poll the session's messages (afterVersion cursor) and push new assistant
//     replies back into the room, in the exact wire shape the web client renders.
//
// The bridge is single-host: it talks to local cores it can reach directly. In a
// real multi-machine deployment each member runs their own bridge beside their
// own cores.
package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
	syncpkg "github.com/luckylhb90/bnbloc/services/bridge/sync"
)

// Config configures the bridge worker.
type Config struct {
	// GatewayBaseURL is the bnbloc gateway, e.g. http://gateway:8080.
	GatewayBaseURL string
	// GatewayToken, if set, is a static bearer for gateway calls. Normally left
	// empty: the bridge mints an owner session token via login so its calls
	// carry the owner's room scope (a room's membership authorizes SSE + activity
	// posts; the fixed daemon subject is not a room member).
	GatewayToken string
	// RefreshToken, if set, is the owner's long-lived refresh token (handed over
	// by the desktop after sign-in). Owner session tokens are minted from it via
	// POST /v1/auth/refresh, which works on SSO-only gateways where direct login
	// is disabled. Without it the bridge falls back to the dev login.
	RefreshToken string
	// OwnerSubject is the member this bridge acts for; only that member's chat
	// messages are claimed.
	OwnerSubject string
	// BridgeID is the stable durable-job worker identity. Empty derives from owner+tenant.
	BridgeID string
	// AppInstanceID identifies the desktop/app instance in heartbeat metadata.
	AppInstanceID string
	// Version is reported in bridge heartbeats for diagnostics.
	Version string
	// DisabledProviders holds "core/provider" keys the user turned off. A
	// disabled pair is never advertised to a room and is refused if a chat
	// still names it. Nil/empty = every detected provider is enabled.
	DisabledProviders map[string]bool
	// ServeAllOwners, when true, makes this bridge drive an agent for a message
	// addressed to ANY member (not just OwnerSubject), using its own local core.
	// This is the single-host dev/test mode where one bridge + one core stands in
	// for every member's machine. In production each member runs their own bridge
	// and this stays false, so a bridge only serves its owner's agents.
	ServeAllOwners bool
	// Tenant is the workspace/org the bridge acts inside. Empty keeps the
	// gateway's default tenant for the owner.
	Tenant string
	// Rooms lists the room ids to watch. Empty means discover from the gateway.
	Rooms []string
	// RelayBaseURL is the bnbloc-relay base for raw sync streams, e.g.
	// http://relay:8085. Empty disables real-time file sync (mutagen mode).
	RelayBaseURL string
	// SyncEngine names the file-sync engine to use for live-sync rooms
	// (default "gosync"). Pluggable: register others under a different name.
	SyncEngine string
	// SyncSecret is a deployment-wide shared secret. The per-room sync auth token
	// is HMAC(SyncSecret, roomId), so only bridges holding the secret can pair on
	// a room's sync stream. Empty means no auth token (dev/single-host only).
	SyncSecret string
	// SyncDirFor returns the local project directory to mirror for a room, or ""
	// to skip. Defaults to the bound agent-session workspace dir.
	SyncDirFor func(roomID string) string
	// Cores lists the configured local-agent cores in priority order. The first
	// is the default core used when a chat names none.
	Cores []CoreConfig
	// WindowChars bounds the verbatim recent-turns window injected per drive
	// (L2). ContextRecentRows caps how many feed rows are pulled before layering.
	// Both are deployment/runtime tuning knobs (internal budget, not a product
	// room setting); 0 uses the built-in default. A future room-level "agent
	// context" setting (summarizer provider/model) can override per room without
	// changing this deployment floor.
	WindowChars       int
	ContextRecentRows int
	// Sandbox is the desktop's agent sandbox (BNBLOC_SANDBOX_*): memory
	// only, its admin token never logged nor handed to a core.
	Sandbox SandboxConfig
}

// CoreConfig describes one configured local-agent core: its name, the provider
// implementation that drives it, the endpoint of the member's local daemon
// (zero until discovered), and whether that endpoint is an explicit,
// never-rediscovered configuration. It is the coreset entry the composition
// root built, so the bridge and gateway share one core description.
type CoreConfig = coreset.Entry

// coreEntry is the bridge's internal per-core state: the provider driving the
// core, the endpoint of its local daemon, and whether that endpoint is fixed
// configuration (never rediscovered).
type coreEntry struct {
	provider corepkg.Provider
	endpoint corepkg.Endpoint
	explicit bool
}

// maxConcurrentClaims bounds how many chat-driven claim goroutines run at once.
// A chat burst (many messages arriving together) would otherwise spawn one
// goroutine per message with no ceiling; this caps the fan-out so the bridge
// can't exhaust memory/FDs against the local core.
const maxConcurrentClaims = 32

// corePollTimeout bounds one status/message poll against a local core. Polls
// are cheap reads; a daemon that cannot answer one in this window is treated as
// a failed poll (counted toward reflow's give-up budget) instead of pinning the
// drive while the job lease keeps renewing. Longer core calls (session create)
// keep the core client's own per-request bound.
const corePollTimeout = 15 * time.Second

// leaseRenewInterval must stay well below the server lease window
// (store.DefaultLeaseMs) so a slow renewal round-trip cannot let the lease
// lapse under a healthy worker.
const leaseRenewInterval = 20 * time.Second

// Bridge runs the chat->agent->reply loop for one member.
type Bridge struct {
	cfg         Config
	cores       map[string]coreEntry // core name -> provider + endpoint
	coreNames   []string             // configured cores in priority order
	defaultCore string               // first configured core (used when none named)
	http        *http.Client
	stream      *http.Client
	// summarizer condenses conversation older than the verbatim window (L1). The
	// default is deterministic and model-free; a configurable LLM-backed
	// summarizer can replace it later behind the same interface.
	summarizer  roomlog.Summarizer
	windowChars int // per-drive verbatim recent-turns budget (chars)
	recentRows  int // how many recent feed rows to pull before layering

	// driveCtx is the Bridge's root context, set in Run. Agent drives (claim +
	// reflow) run under it, NOT under a room's SSE stream context — a roster
	// change re-subscribes the stream (cancelling streamCtx), and an in-flight
	// drive/reflow must survive that so a long turn's reply is not lost.
	driveCtx context.Context
	mu       sync.Mutex
	rooms    map[string]context.CancelFunc // roomId -> stream canceller
	sessions map[string]*roomSession       // "roomId\x00core\x00provider" -> bound session
	syncs    map[string]context.CancelFunc // roomId -> sync engine canceller
	// gone holds rooms the gateway answered 404/403 for (deleted, or the owner
	// lost access) and when: reconcile does not re-subscribe them while they
	// are in cooldown, so a room that discovery still lists (or a static
	// BNBLOC_BRIDGE_ROOMS entry) is not re-requested in a loop.
	gone     map[string]time.Time
	claimSem chan struct{} // bounds concurrent claim goroutines
	started  int64
	// rosterMu guards lastRoster (the last ?agents= advertised) and targetCache.
	// When the live roster changes (a core came up/down, targets changed), room
	// streams are re-subscribed so members see the current agents without a
	// restart.
	rosterMu    sync.Mutex
	lastRoster  string
	rosterInit  bool
	targetCache map[string][]corepkg.Target // core -> last-known targets (roster stability)

	tokMu      sync.Mutex
	ownerTok   string
	ownerTokAt time.Time
	// refreshTok is the newest refresh token of the bridge's OWN family
	// (rotated on every refresh). forked records that the handed-over
	// Config.RefreshToken was already forked into that family, so it is never
	// presented again (see gatewayAuth).
	refreshTok string
	forked     bool

	// liveMu + live hold the most recent live-artifact gestures per room: what
	// members did inside running artifacts (game moves, diagram annotations,
	// dashboard filters) that the room stream carried as artifact_state. This
	// is how an agent SEES interaction with its deliverables: the gestures are
	// materialized into .room/live.md each turn, so "make it harder" or "why
	// did the chart go empty" is answerable with the actual interaction trail
	// instead of guesses. Bounded per room; transient by design (a restart
	// starts a fresh trail — artifact versions remain the durable record).
	liveMu sync.Mutex
	live   map[string][]liveGesture

	// running tracks in-flight jobs so a cancellation arriving over the room
	// stream interrupts the local turn immediately, rather than waiting for the
	// next lease renewal to discover the job is no longer ours.
	runMu   sync.Mutex
	running map[string]*runningJob

	// sandbox is the agent sandbox this bridge drives sandboxed turns in
	// (sandbox.go); never nil (an unconfigured one is simply never ready).
	sandbox *sandboxRuntime

	// contrib holds the registered session contributors (profile.go).
	contrib contributors
}

type roomSession struct {
	owner       string // agent owner this session serves (e.g. "jack")
	provider    string // agent provider (harness) this session serves (e.g. "codex")
	core        string // core name this session is bound to
	workspaceID string
	sessionID   string
	lastVersion int64 // last reflowed message cursor
	// lastPublished is the session workspace's file listing as of the previous
	// deliverable publish (nil before the first observed turn). The diff against
	// the current manifest is what changed since.
	lastPublished map[string]fileStamp
	// turn is the in-flight turn: when it began (deliverable publishing scopes
	// a cold-start session to this turn's own output) and the job + prompting
	// message its published versions link to. Guarded by mu (read by sync
	// planning on another goroutine).
	turn deliverableTurn
	// publishMu serializes deliverable publishes for this session: the
	// post-turn publish runs on its own goroutine while the next turn's reflow
	// publishes periodically, and both read+replace lastPublished.
	publishMu sync.Mutex
	// ctx tracks the pull-based room context materialized into the session
	// workspace's .room/ directory: which history chunks exist and how far
	// they reach. Zero value = nothing materialized yet.
	ctx          roomlog.CtxState
	stateVersion int64 // monotonic version for agent_session_state reports
	// profileSum is the content checksum of the agent profile the session
	// was created with ("" = none); a turn whose profile differs gets a new
	// session (profile.go).
	profileSum string
	// hooks are the session contributors' turn/release callbacks (guarded
	// by mu).
	hooks sessionHooks
	mu    sync.Mutex
}

func (rs *roomSession) markTurn(t deliverableTurn) {
	rs.mu.Lock()
	rs.turn = t
	rs.mu.Unlock()
}

func (rs *roomSession) lastTurn() deliverableTurn {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.turn
}

func (rs *roomSession) lastTurnStart() int64 { return rs.lastTurn().StartMs }

// runningJob is a claimed job this bridge is executing. The bound session is
// filled in once the drive creates it; stop cancels the drive context.
type runningJob struct {
	mu      sync.Mutex
	core    string
	session *roomSession
	abort   bool
	stop    context.CancelFunc
}

// bind records the core session a job is driving, so an abort can interrupt it.
func (r *runningJob) bind(core string, rs *roomSession) {
	r.mu.Lock()
	r.core, r.session = core, rs
	r.mu.Unlock()
}

func (r *runningJob) bound() (string, *roomSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.core, r.session
}

// markAborted flags the job as stopped by the server and reports whether this
// call was the first to do so, so an abort runs its interrupt exactly once.
func (r *runningJob) markAborted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abort {
		return false
	}
	r.abort = true
	return true
}

func (r *runningJob) aborted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abort
}

// trackJob registers an in-flight job and returns a release function.
func (b *Bridge) trackJob(jobID string, rj *runningJob) func() {
	if jobID == "" {
		return func() {}
	}
	b.runMu.Lock()
	if b.running == nil {
		b.running = map[string]*runningJob{}
	}
	b.running[jobID] = rj
	b.runMu.Unlock()
	return func() {
		b.runMu.Lock()
		delete(b.running, jobID)
		b.runMu.Unlock()
	}
}

// abortJob stops a job this bridge is running because the server says it is no
// longer ours. It interrupts the core turn first — that is what actually stops
// the agent burning time and tokens — then cancels the drive so the worker slot
// is released.
func (b *Bridge) abortJob(jobID, status string) {
	b.runMu.Lock()
	rj := b.running[jobID]
	b.runMu.Unlock()
	// A cancel can arrive from the room stream and the lease loop at once; only
	// the first caller interrupts.
	if rj == nil || !rj.markAborted() {
		return
	}
	slog.Info("bnbloc bridge aborting job",
		"event", "bnbloc.bridge.job_aborted", "job", jobID, "status", status)
	if core, rs := rj.bound(); rs != nil {
		b.interruptTurn(core, rs)
	}
	if rj.stop != nil {
		rj.stop()
	}
}

// Default per-drive context budgets. They are deployment tuning floors
// (BNBLOC_BRIDGE_CONTEXT_WINDOW_CHARS / _RECENT_ROWS override them), not product
// room settings — see Config.WindowChars.
const (
	defaultWindowChars = 12000 // recent turns kept verbatim (chars, not tokens)
	defaultRecentRows  = 200   // feed rows pulled before layering
)

// New constructs a Bridge.
func New(cfg Config) *Bridge {
	if cfg.SyncEngine == "" {
		cfg.SyncEngine = "gosync"
	}
	cores := make(map[string]coreEntry, len(cfg.Cores))
	names := make([]string, 0, len(cfg.Cores))
	for _, c := range cfg.Cores {
		cores[c.Name] = coreEntry{provider: c.Provider, endpoint: c.Endpoint, explicit: c.Explicit}
		names = append(names, c.Name)
	}
	defaultCore := ""
	if len(names) > 0 {
		defaultCore = names[0]
	}
	b := &Bridge{
		cfg:         cfg,
		cores:       cores,
		coreNames:   names,
		defaultCore: defaultCore,
		http:        &http.Client{Timeout: 20 * time.Second},
		stream:      &http.Client{},
		rooms:       map[string]context.CancelFunc{},
		sessions:    map[string]*roomSession{},
		syncs:       map[string]context.CancelFunc{},
		gone:        map[string]time.Time{},
		claimSem:    make(chan struct{}, maxConcurrentClaims),
		started:     time.Now().UnixMilli(),
		windowChars: firstPositive(cfg.WindowChars, defaultWindowChars),
		recentRows:  firstPositive(cfg.ContextRecentRows, defaultRecentRows),
	}
	// Default L1 summarizer is deterministic and model-free, so every agent
	// derives the same story-so-far from the same feed (no shared persistence).
	// A room-configurable LLM summarizer can replace this later.
	b.summarizer = roomlog.StructuredSummarizer{}
	b.sandbox = newSandboxRuntime(cfg.Sandbox)
	return b
}

// Run watches rooms for roster liveness and drives durable bridge jobs until ctx is cancelled.
func (b *Bridge) Run(ctx context.Context) {
	b.driveCtx = ctx
	slog.Info("bnbloc bridge started",
		"event", "bnbloc.bridge.started",
		"bridge", b.bridgeID(), "owner", b.cfg.OwnerSubject)
	reconcileTicker := time.NewTicker(10 * time.Second)
	defer reconcileTicker.Stop()
	// Prewarm: probe the agent sandbox (the desktop prepared its machine)
	// before the first advertisement, so sandboxed agents are badged at once.
	// Resolve endpoints and list targets first, so the first room subscription
	// already advertises the real roster instead of churning once it lands.
	b.sandbox.status(ctx)
	b.refreshRoster(ctx)
	b.reconcile(ctx)
	if err := b.reportHeartbeat(ctx); err != nil {
		slog.Warn("bnbloc bridge heartbeat failed", "event", "bnbloc.bridge.heartbeat_failed", "error", err)
	}
	go b.heartbeatLoop(ctx)
	go b.claimLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			b.releaseSession()
			return
		case <-reconcileTicker.C:
			b.reconcile(ctx)
			b.refreshRoster(ctx)
		}
	}
}

func (b *Bridge) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.reportHeartbeat(ctx); err != nil {
				slog.Warn("bnbloc bridge heartbeat failed", "event", "bnbloc.bridge.heartbeat_failed", "error", err)
			}
		}
	}
}

// claimLoop pulls durable jobs continuously. It claims as many jobs as the
// bridge has free execution slots and runs each in its own goroutine, so one
// long agent turn never blocks the other agents this bridge serves.
func (b *Bridge) claimLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		// Claim again immediately while the queue keeps handing out work, so a
		// burst drains at execution speed rather than one job per tick.
		for b.claimOnce(ctx) && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// freeSlots reports how many jobs this bridge can still start without exceeding
// its execution ceiling. Claiming more than this would hold leases on jobs that
// sit unstarted while another bridge could have run them.
func (b *Bridge) freeSlots() int {
	return cap(b.claimSem) - len(b.claimSem)
}

func (b *Bridge) bridgeID() string {
	if id := strings.TrimSpace(b.cfg.BridgeID); id != "" {
		return id
	}
	owner := strings.TrimSpace(b.cfg.OwnerSubject)
	if owner == "" {
		owner = "unknown"
	}
	tid := strings.TrimSpace(b.cfg.Tenant)
	if tid == "" {
		tid = "default"
	}
	return "bridge-" + owner + "-" + tid
}

type heartbeatRequest struct {
	BridgeID       string           `json:"bridgeId"`
	TenantID       string           `json:"tenantId"`
	OwnerUserID    string           `json:"ownerUserId"`
	AppInstanceID  string           `json:"appInstanceId"`
	Version        string           `json:"version"`
	ServeAllOwners bool             `json:"serveAllOwners"`
	Agents         []heartbeatAgent `json:"agents"`
}

type heartbeatAgent struct {
	Core             string          `json:"core"`
	Provider         string          `json:"provider"`
	TargetID         string          `json:"targetId"`
	ModelOptionsJSON json.RawMessage `json:"modelOptions"`
	Disabled         bool            `json:"disabled"`
}

func (b *Bridge) reportHeartbeat(ctx context.Context) error {
	_, err := b.gatewayPOST(ctx, "/v1/bridge/heartbeat", heartbeatRequest{
		BridgeID:       b.bridgeID(),
		TenantID:       b.cfg.Tenant,
		OwnerUserID:    b.cfg.OwnerSubject,
		AppInstanceID:  b.cfg.AppInstanceID,
		Version:        b.cfg.Version,
		ServeAllOwners: b.cfg.ServeAllOwners,
		Agents:         b.heartbeatAgents(),
	})
	return err
}

// heartbeatAgents reports the roster refreshRoster last listed. It never lists
// targets itself: one ListTargets pass per tick (refreshRoster) feeds both the
// heartbeat and the ?agents= advertisement.
func (b *Bridge) heartbeatAgents() []heartbeatAgent {
	var out []heartbeatAgent
	for _, name := range b.coreNames {
		for _, t := range b.cachedTargets(name) {
			models, _ := json.Marshal(map[string]any{
				"models":           t.Models,
				"reasoningEfforts": t.ReasoningEfforts,
				"reasoningByModel": t.ReasoningByModel,
				"defaultModel":     t.DefaultModel,
				"defaultReasoning": t.DefaultReasoning,
			})
			out = append(out, heartbeatAgent{Core: name, Provider: t.Provider, TargetID: t.ID, ModelOptionsJSON: models, Disabled: b.providerDisabled(name, t.Provider)})
		}
	}
	return out
}

type claimRequest struct {
	BridgeID       string `json:"bridgeId"`
	TenantID       string `json:"tenantId"`
	OwnerUserID    string `json:"ownerUserId"`
	MaxJobs        int    `json:"maxJobs"`
	ServeAllOwners bool   `json:"serveAllOwners"`
}

type claimResponse struct {
	Jobs []bridgeJob `json:"jobs"`
}

// jobRef identifies the claimed job a drive runs for: its id, the chat
// message that prompted it (the source link of published deliverables), the
// human who asked (the requester whose authority the turn runs under) and
// the tenant it runs in.
type jobRef struct {
	ID              string
	MessageSeq      int64
	RequesterUserID string
	TenantID        string
	// content is what the turn wears (experts, skill packs: content.go);
	// nil = none.
	content *jobContent
}

type bridgeJob struct {
	JobID           string
	TenantID        string
	RoomID          string
	ChannelID       string
	MessageSeq      int64 // the prompting chat message
	RequesterUserID string
	OwnerUserID     string
	Core            string
	Provider        string
	TargetID        string
	MessageText     string
	AttachmentIDs   []string
	Model           string
	ReasoningEffort string
	// ExpertID / ExpertVersion / ExpertName: the expert the chat asked the
	// agent to wear, pinned on the job (content.go).
	ExpertID      string
	ExpertVersion string
	ExpertName    string
}

func (j *bridgeJob) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	j.JobID = jsonString(raw, "jobId", "JobID")
	j.TenantID = jsonString(raw, "tenantId", "TenantID")
	j.RoomID = jsonString(raw, "roomId", "RoomID")
	j.ChannelID = jsonString(raw, "channelId", "ChannelID")
	if rawSeq, ok := raw["messageSeq"]; ok {
		_ = json.Unmarshal(rawSeq, &j.MessageSeq)
	}
	j.RequesterUserID = jsonString(raw, "requesterUserId", "RequesterUserID")
	j.OwnerUserID = jsonString(raw, "ownerUserId", "OwnerUserID")
	j.Core = jsonString(raw, "core", "Core")
	j.Provider = jsonString(raw, "provider", "Provider")
	j.TargetID = jsonString(raw, "targetId", "TargetID")
	j.MessageText = jsonString(raw, "messageText", "MessageText")
	if rawIDs, ok := raw["attachmentIds"]; ok {
		_ = json.Unmarshal(rawIDs, &j.AttachmentIDs)
	}
	j.Model = jsonString(raw, "model", "Model")
	j.ReasoningEffort = jsonString(raw, "reasoningEffort", "ReasoningEffort")
	j.ExpertID = jsonString(raw, "expertId")
	j.ExpertVersion = jsonString(raw, "expertVersion")
	j.ExpertName = jsonString(raw, "expertName")
	return nil
}

func jsonString(raw map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		if b, ok := raw[name]; ok {
			var v string
			if json.Unmarshal(b, &v) == nil {
				return v
			}
		}
	}
	return ""
}

func (b *Bridge) claimOnce(ctx context.Context) bool {
	slots := b.freeSlots()
	if slots <= 0 {
		return false
	}
	raw, err := b.gatewayPOST(ctx, "/v1/bridge/jobs/claim", claimRequest{BridgeID: b.bridgeID(), TenantID: b.cfg.Tenant, OwnerUserID: b.cfg.OwnerSubject, MaxJobs: slots, ServeAllOwners: b.cfg.ServeAllOwners})
	if err != nil {
		slog.Warn("bnbloc bridge job claim failed", "event", "bnbloc.bridge.job_claim_failed", "error", err)
		return false
	}
	var resp claimResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		slog.Warn("bnbloc bridge job claim decode failed", "event", "bnbloc.bridge.job_claim_decode_failed", "error", err)
		return false
	}
	// Drives run under the Bridge root context, not the claim caller's: a roster
	// change cancels stream contexts and must never abort a running turn.
	driveCtx := b.driveCtx
	if driveCtx == nil {
		driveCtx = ctx
	}
	for _, job := range resp.Jobs {
		select {
		case b.claimSem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		go func(job bridgeJob) {
			defer func() { <-b.claimSem }()
			b.runBridgeJob(driveCtx, job)
		}(job)
	}
	return len(resp.Jobs) > 0
}

func (b *Bridge) runBridgeJob(ctx context.Context, job bridgeJob) {
	// Hold the lease for as long as the turn actually runs. Without renewal a
	// turn longer than the server lease is reclaimed by another bridge and the
	// same prompt executes twice.
	ctx, stopDrive := context.WithCancel(ctx)
	defer stopDrive()
	// When this turn began: scopes deliverable publishing to what this turn
	// actually wrote (a cold-start session must not dump its older history).
	turnStartMs := time.Now().UnixMilli()
	// Register the job so a cancellation arriving over the room stream can
	// interrupt this turn at once. The lease loop is the fallback for when the
	// stream is down: slower, but it still stops the work.
	rj := &runningJob{stop: stopDrive}
	release := b.trackJob(job.JobID, rj)
	defer release()
	go b.renewLeaseLoop(ctx, job.JobID, func() { b.abortJob(job.JobID, "lease_lost") })
	text := strings.TrimSpace(job.MessageText)
	if text == "" && len(job.AttachmentIDs) == 0 {
		b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureRunnerFailed, map[string]any{"error": "missing messageText"})
		return
	}
	owner := firstNonEmpty(job.OwnerUserID, b.cfg.OwnerSubject)
	coreName, ok := b.resolveCore(job.Core)
	if !ok {
		// A job naming a core this bridge does not run must not be driven on a
		// different core: that silently answers with the wrong agent.
		b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureAgentNotAdvertised, map[string]any{"core": job.Core, "provider": job.Provider})
		return
	}
	if b.providerDisabled(coreName, job.Provider) {
		b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureAgentNotAdvertised, map[string]any{"core": coreName, "provider": job.Provider})
		return
	}
	// The expert and skill packs the turn wears are resolved and verified
	// before anything else: content from someone else that runs scripts
	// forces the sandbox, decided next.
	content, ok := b.jobContentOrRefuse(ctx, job, owner, coreName)
	if !ok {
		return
	}
	// Where the drive runs (host or agent sandbox) is decided before any
	// quota is reserved: a borrowed drive the tenant requires to be
	// sandboxed, or a turn wearing someone else's script-bearing content,
	// is refused here when the sandbox cannot take it.
	borrowed := isBorrow(job.RequesterUserID, owner)
	prepareExec, refuse, unsandboxed := b.planSandbox(ctx, job, owner, coreName, borrowed, content.foreign)
	if refuse != "" {
		b.refuseUnsandboxed(ctx, job, owner, coreName, refuse)
		return
	}
	// A borrow runs only on a positive authorization. When this bridge acts
	// as the owner it reserves the run (R8: the reservation counts against
	// the quota before anything is driven) and settles it after the drive.
	// The reservation says whether the drive must be sandboxed (a platform
	// agent): one that is not is refused like the tenant policy's, before
	// anything runs.
	var reservationID string
	if borrowed {
		rsv := b.authorizeBorrow(ctx, job, owner, coreName, prepareExec != nil)
		reservationID = rsv.ID
		denyReason := rsv.DenyReason
		if denyReason == shareagent.DenySandboxRequired {
			b.refuseUnsandboxed(ctx, job, owner, coreName, platformSandboxPrefix+unsandboxed)
			return
		}
		if denyReason != "" {
			b.postBorrowDenied(ctx, job.JobID, job.RoomID, owner, job.Provider, job.ChannelID, denyReason)
			b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureBorrowNotAuthorized, map[string]any{"core": coreName, "provider": job.Provider, "requesterUserId": job.RequesterUserID, "denyReason": denyReason})
			return
		}
		// A platform agent's turn that named no model runs with its host's
		// default model, exactly as if the member had picked it: the session
		// options, the run report and the settle all read job.Model.
		if job.Model == "" {
			job.Model = rsv.DefaultModel
		}
	}
	target := b.resolveTarget(ctx, coreName, job.Provider)
	if jt := strings.TrimSpace(job.TargetID); jt != "" && jt != job.Provider && jt != coreName+"/"+job.Provider {
		target = jt
	}
	running := map[string]any{"core": coreName, "provider": job.Provider, "targetId": target, "sandboxed": prepareExec != nil}
	if content.expert != nil {
		running["expert"] = expertLabel(content.expert)
	}
	if len(content.missing) > 0 {
		running["contentMissing"] = content.missing
	}
	if err := b.reportJobStatus(ctx, job.JobID, store.AgentJobRunning, "", running); err != nil {
		slog.Debug("bnbloc bridge job running status failed", "event", "bnbloc.bridge.job_status_failed", "job", job.JobID, "error", err)
	}
	opts := corepkg.SessionOptions{Model: job.Model, ReasoningEffort: job.ReasoningEffort}
	reflowReported := false
	res, err := b.driveClaim(ctx, jobRef{ID: job.JobID, MessageSeq: job.MessageSeq, RequesterUserID: job.RequesterUserID, TenantID: job.TenantID, content: content}, job.RoomID, owner, coreName, job.Provider, target, text, job.AttachmentIDs, opts, prepareExec, job.ChannelID, func(rs *roomSession) {
		// Publish the bound session so a cancel arriving mid-turn can interrupt it.
		rj.bind(coreName, rs)
		reflowReported = true
		b.reportJobStatus(ctx, job.JobID, store.AgentJobReflowing, "", map[string]any{"core": coreName, "provider": job.Provider, "workspaceId": rs.workspaceID, "sessionId": rs.sessionID})
	})
	// A reserved borrow is settled once the drive ends — whatever its
	// outcome; the run was counted at reserve time — with the usage the
	// turn's replies reported. Detached context: ctx is cancelled the moment
	// this function returns (or the job is aborted).
	if reservationID != "" {
		recordCtx := b.driveCtx
		if recordCtx == nil {
			recordCtx = context.WithoutCancel(ctx)
		}
		go b.settleBorrow(recordCtx, job.RoomID, owner, coreName, job.Provider, reservationID, res.usage.recordFields(job.Model))
	}
	// An aborted job is no longer ours: the server already recorded the cancel and
	// cleared the lease, so any outcome report would be rejected. Stop reporting
	// rather than arguing with the server about a turn nobody wants.
	if rj.aborted() {
		slog.Info("bnbloc bridge job abandoned after abort",
			"event", "bnbloc.bridge.job_abandoned", "job", job.JobID)
		return
	}
	if err != nil {
		if res.replied {
			b.reportJobStatus(ctx, job.JobID, store.AgentJobReplied, "", res.usage.withRunReport(map[string]any{"core": coreName, "provider": job.Provider, "workspaceId": res.workspaceID, "sessionId": res.sessionID, "warning": cleanDriveError(err)}, job.Model))
			return
		}
		reason := classifyJobFailure(err)
		b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, reason, res.usage.withRunReport(map[string]any{"core": coreName, "provider": job.Provider, "targetId": target, "error": cleanDriveError(err)}, job.Model))
		return
	}
	if !reflowReported {
		b.reportJobStatus(ctx, job.JobID, store.AgentJobReflowing, "", map[string]any{"core": coreName, "provider": job.Provider})
	}
	if res.replied {
		b.reportJobStatus(ctx, job.JobID, store.AgentJobReplied, "", res.usage.withRunReport(map[string]any{"core": coreName, "provider": job.Provider, "workspaceId": res.workspaceID, "sessionId": res.sessionID}, job.Model))
		// The turn's changed files become artifact versions so the room's Docs
		// panel reflects the agent's work. Presentation only: a failure never
		// marks the turn failed.
		if _, boundRS := rj.bound(); boundRS != nil {
			// Detached context: runBridgeJob's ctx is cancelled the moment this
			// function returns (defer stopDrive), which would abort the publish
			// goroutine's very first request. The bridge root keeps it alive.
			go b.publishDeliverables(b.driveCtx, job.RoomID, job.ChannelID, boundRS,
				deliverableTurn{StartMs: turnStartMs, JobID: job.JobID, SourceSeq: job.MessageSeq})
		}
		return
	}
	b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureReplyTimeout, res.usage.withRunReport(map[string]any{"core": coreName, "provider": job.Provider, "workspaceId": res.workspaceID, "sessionId": res.sessionID, "error": "agent turn completed without a visible reply"}, job.Model))
}

// renewLeaseLoop keeps this bridge's lease on a running job alive until the
// drive finishes and the caller cancels ctx.
//
// A failed renewal means this worker no longer owns the job: it was cancelled by
// a member, or taken over after the lease lapsed. Either way the local turn is
// now unwanted work, so the loop stops driving and reports the loss to its
// caller, which interrupts the core.
func (b *Bridge) renewLeaseLoop(ctx context.Context, jobID string, lost func()) {
	if strings.TrimSpace(jobID) == "" {
		return
	}
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := b.gatewayPOST(ctx, "/v1/bridge/jobs/"+url.PathEscape(jobID)+"/lease", map[string]any{"bridgeId": b.bridgeID()}); err != nil {
				slog.Info("bnbloc bridge job lease lost, interrupting turn",
					"event", "bnbloc.bridge.job_lease_lost", "job", jobID, "error", err)
				if lost != nil {
					lost()
				}
				return
			}
		}
	}
}

// interruptTurn stops an in-flight turn on the core that is running it. Cores
// that cannot be interrupted keep running; the job is no longer ours either way,
// so the turn's result is discarded.
func (b *Bridge) interruptTurn(coreName string, rs *roomSession) {
	if rs == nil {
		return
	}
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	interrupter, ok := corepkg.As[corepkg.TurnInterrupter](ctx, entry.provider, entry.endpoint, corepkg.CapabilityInterrupt)
	if !ok {
		slog.Debug("bnbloc bridge core cannot interrupt turns",
			"event", "bnbloc.bridge.interrupt_unsupported", "core", coreName)
		return
	}
	if err := interrupter.Interrupt(ctx, entry.endpoint, rs.workspaceID, rs.sessionID); err != nil {
		slog.Warn("bnbloc bridge turn interrupt failed",
			"event", "bnbloc.bridge.interrupt_failed", "core", coreName, "session", rs.sessionID, "error", err)
		return
	}
	slog.Info("bnbloc bridge turn interrupted",
		"event", "bnbloc.bridge.turn_interrupted", "core", coreName, "session", rs.sessionID)
}

type driveResult struct {
	workspaceID string
	sessionID   string
	replied     bool
	// usage is what the turn's consumed replies reported (borrow record).
	usage turnUsage
}

func (b *Bridge) reportJobStatus(ctx context.Context, jobID string, status store.AgentJobStatus, reason store.AgentJobFailureReason, diagnostics map[string]any) error {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	body := map[string]any{"bridgeId": b.bridgeID(), "status": status, "failureReason": reason}
	if diagnostics != nil {
		body["diagnostics"] = diagnostics
	}
	_, err := b.gatewayPOST(ctx, "/v1/bridge/jobs/"+url.PathEscape(jobID)+"/status", body)
	return err
}

func classifyJobFailure(err error) store.AgentJobFailureReason {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "killed") || strings.Contains(msg, "signal") {
		return store.FailureRunnerKilled
	}
	return store.FailureRunnerFailed
}

// reconcile subscribes to any watched room not already streamed.
func (b *Bridge) reconcile(ctx context.Context) {
	rooms := b.cfg.Rooms
	// hosted are the rooms the owner is only a platform agent's host in: the
	// bridge drives the hosted agent there but never live-syncs the owner's
	// workspace with the room's members.
	var hosted map[string]bool
	if len(rooms) == 0 {
		var ok bool
		if rooms, hosted, ok = b.discoverRooms(ctx); !ok {
			// A failed listing says nothing about membership: keep the current
			// set rather than tearing every stream down and re-subscribing all
			// of them on the next tick.
			return
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Build the desired set for O(1) membership checks.
	want := make(map[string]struct{}, len(rooms))
	for _, room := range rooms {
		want[room] = struct{}{}
	}
	// A gone room leaves the cooldown when its time is up or when it is no
	// longer listed (so the set never outgrows the watched rooms).
	now := time.Now()
	for room, at := range b.gone {
		if _, listed := want[room]; !listed || now.Sub(at) >= goneRoomCooldown {
			delete(b.gone, room)
		}
	}
	// If any discovered room is new (created/joined after our cached owner token
	// was minted), that token lacks its room scope and the room's SSE
	// subscription would 403. Invalidate the token so the next gateway call
	// re-logs in and picks up the new membership before we subscribe.
	for _, room := range rooms {
		if _, ok := b.rooms[room]; !ok && !b.isGone(room) {
			b.invalidateToken()
			break
		}
	}
	// Start streams (and syncs) for any newly-watched room.
	for _, room := range rooms {
		if b.isGone(room) {
			continue
		}
		if _, ok := b.rooms[room]; !ok {
			streamCtx, cancel := context.WithCancel(ctx)
			b.rooms[room] = cancel
			go b.streamRoom(streamCtx, room)
		}
		if hosted[room] {
			if syncCancel, ok := b.syncs[room]; ok {
				syncCancel()
				delete(b.syncs, room)
			}
			continue
		}
		if b.cfg.RelayBaseURL != "" {
			if _, ok := b.syncs[room]; !ok {
				syncCtx, cancel := context.WithCancel(ctx)
				b.syncs[room] = cancel
				go b.maybeStartSync(syncCtx, room)
			}
		}
	}
	// Drop rooms that disappeared (deleted, or the owner left): cancel their
	// stream + sync contexts and delete the map entries so the bridge stops
	// reconnecting to dead rooms. Also prune their per-provider claim locks so
	// roomLocks does not grow without bound across a long-lived bridge.
	for room, cancel := range b.rooms {
		if _, ok := want[room]; ok {
			continue
		}
		cancel()
		delete(b.rooms, room)
		if syncCancel, ok := b.syncs[room]; ok {
			syncCancel()
			delete(b.syncs, room)
		}
		b.forgetRoom(room)
	}
}

// refreshRoster keeps the advertised agent roster and core endpoints live
// without a bridge restart. Each tick it (1) (re)resolves the endpoint of every
// discovery-capable core through coreset.Rediscover — picking up a daemon that
// came up after boot AND following one that restarted onto a new random
// port/token, hot-swapping the endpoint and dropping that core's now-orphaned
// sessions — then (2) lists each core's targets once (the cache the heartbeat
// and the ?agents= advertisement both read) and, if the roster changed since
// the last advertisement, drops the active room streams so the following
// reconcile re-subscribes each room with the current ?agents=.
func (b *Bridge) refreshRoster(ctx context.Context) {
	b.rediscoverCores(ctx)
	b.refreshTargets(ctx)
	b.sandbox.status(ctx) // cached; re-probes once its window lapses

	roster := b.rosterKey(ctx)
	b.rosterMu.Lock()
	changed := !b.rosterInit || roster != b.lastRoster
	b.lastRoster = roster
	b.rosterInit = true
	b.rosterMu.Unlock()
	if !changed {
		return
	}
	b.mu.Lock()
	stale := make([]context.CancelFunc, 0, len(b.rooms))
	for room, cancel := range b.rooms {
		stale = append(stale, cancel)
		delete(b.rooms, room)
	}
	b.mu.Unlock()
	slog.Info("bnbloc bridge roster changed, re-advertising",
		"event", "bnbloc.bridge.roster_changed", "agents", roster)
	for _, cancel := range stale {
		cancel()
	}
}

// rediscoverCores re-resolves every core's endpoint with the shared
// coreset.Rediscover policy (explicit endpoints are fixed; a transient miss
// keeps the last-known one). Discovery runs WITHOUT b.mu held — a slow probe
// must not block every drive's core lookup — and the result is written back
// only if the entry was not changed meanwhile. A changed address means the
// daemon restarted: sessions bound to that core have no live runtime there
// any more, so they are dropped and the next chat creates fresh ones.
func (b *Bridge) rediscoverCores(ctx context.Context) {
	for _, name := range b.coreNames {
		before, ok := b.coreEntry(name)
		if !ok {
			continue
		}
		e := coreset.Entry{Name: name, Provider: before.provider, Endpoint: before.endpoint, Explicit: before.explicit}
		found := coreset.Rediscover(ctx, &e)
		if found == before.endpoint {
			continue
		}
		b.mu.Lock()
		cur := b.cores[name]
		if cur.endpoint != before.endpoint {
			b.mu.Unlock()
			continue // changed concurrently; the next tick re-evaluates
		}
		cur.endpoint = found
		b.cores[name] = cur
		rebound := before.endpoint.OK()
		if rebound {
			for key, rs := range b.sessions {
				if rs.core == name {
					b.unbindLocked(key)
				}
			}
		}
		b.mu.Unlock()
		if rebound {
			slog.Info("bnbloc bridge core endpoint changed, rebinding",
				"event", "bnbloc.bridge.core_endpoint_changed", "core", name, "addr", found.BaseURL)
		} else {
			slog.Info("bnbloc bridge core resolved",
				"event", "bnbloc.bridge.core_resolved", "core", name, "addr", found.BaseURL)
		}
	}
}

// refreshTargets lists each reachable core's targets once and caches them. A
// transient miss (endpoint mid-rediscovery, ListTargets error/timeout) keeps
// the last-known targets: a changed roster tears down and re-subscribes room
// streams, so the advertised set must stay stable while a daemon blips.
func (b *Bridge) refreshTargets(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, name := range b.coreNames {
		entry, ok := b.coreEntry(name)
		if !ok || !entry.endpoint.OK() {
			continue
		}
		if ts, err := entry.provider.ListTargets(listCtx, entry.endpoint); err == nil {
			b.cacheTargets(name, ts)
		}
	}
}

// forgetRoom drops per-room state for a room the bridge no longer watches: its
// bound sessions and the per-(core,provider) claim locks in roomLocks. Both are
// keyed "roomId\x00core\x00provider", so pruning by the "roomId\x00" prefix
// clears every core+provider entry the room ever drove. Callers hold b.mu.
func (b *Bridge) forgetRoom(room string) {
	prefix := room + "\x00"
	for key := range b.sessions {
		if strings.HasPrefix(key, prefix) {
			b.unbindLocked(key)
		}
	}
	roomLocks.Range(func(k, _ any) bool {
		if key, ok := k.(string); ok && strings.HasPrefix(key, prefix) {
			roomLocks.Delete(key)
		}
		return true
	})
}

// goneRoomCooldown is how long a room the gateway rejected as gone stays
// unsubscribed while it is still listed. A deleted room leaves discovery within
// one reconcile tick; the cooldown only bounds the retry rate when the listing
// disagrees (or the room comes from the static BNBLOC_BRIDGE_ROOMS list).
const goneRoomCooldown = time.Minute

// roomGone reports a gateway answer that means the room no longer exists for
// this owner: 404 (deleted) or 403 (not a member any more).
func roomGone(err error) bool {
	var ge *gatewayError
	return errors.As(err, &ge) && (ge.Status == http.StatusNotFound || ge.Status == http.StatusForbidden)
}

// isGone reports whether room is in its gone cooldown. Callers hold b.mu.
func (b *Bridge) isGone(room string) bool {
	_, ok := b.gone[room]
	return ok
}

// dropGoneRoom stops watching a room the gateway reports gone: its stream and
// sync are cancelled, its per-room state forgotten, and it enters the gone
// cooldown so reconcile does not immediately re-subscribe it.
func (b *Bridge) dropGoneRoom(room string, err error) {
	b.mu.Lock()
	if cancel, ok := b.rooms[room]; ok {
		cancel()
		delete(b.rooms, room)
	}
	if cancel, ok := b.syncs[room]; ok {
		cancel()
		delete(b.syncs, room)
	}
	b.forgetRoom(room)
	if b.gone == nil {
		b.gone = map[string]time.Time{}
	}
	b.gone[room] = time.Now()
	b.mu.Unlock()
	slog.Warn("bnbloc bridge dropped a room the gateway reports gone",
		"event", "bnbloc.bridge.room_gone", "room", room, "error", err)
}

// discoverRooms lists the owner's rooms from the gateway, with the subset the
// owner only hosts a platform agent in. ok is false when the listing failed
// (as opposed to the owner having no rooms).
func (b *Bridge) discoverRooms(ctx context.Context) (rooms []string, hosted map[string]bool, ok bool) {
	// allTenants=1: the bridge subscribes/advertises/claims in every room its
	// owner is a member of. The owner's session token is minted in whatever
	// tenant their login landed in; without this the tenant-scoped listing
	// hides their rooms in other tenants, the bridge never subscribes, and the
	// room's agents show offline for everyone (including the owner's own
	// browser sessions). includeAgentHost=1: also the rooms where the owner
	// holds a platform agent host's membership (agentHostRooms), which no
	// other listing shows.
	raw, err := b.gatewayGET(ctx, "/v1/users/"+url.PathEscape(b.cfg.OwnerSubject)+"/rooms?allTenants=1&includeAgentHost=1")
	if err != nil {
		return nil, nil, false
	}
	var parsed struct {
		Rooms          []string `json:"rooms"`
		AgentHostRooms []string `json:"agentHostRooms"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return nil, nil, false
	}
	hosted = make(map[string]bool, len(parsed.AgentHostRooms))
	for _, room := range parsed.AgentHostRooms {
		hosted[room] = true
	}
	return parsed.Rooms, hosted, true
}

// maybeStartSync starts a file-sync engine for a room whose syncMode is
// "mutagen" (the live-sync option). It dials the relay raw stream keyed by the
// roomId so both members' bridges pair, runs the configured engine against the
// room's project directory, and reconnects with backoff. Rooms in git/none mode
// are skipped.
func (b *Bridge) maybeStartSync(ctx context.Context, room string) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		mode, dir, err := b.syncPlan(ctx, room)
		if roomGone(err) {
			// The room detail is tenant-scoped to the bridge's session while
			// the listing (allTenants=1) and the room stream span every tenant
			// the owner is in, so a 404/403 here does not mean the room is
			// gone — dropping it on this answer made every room of another
			// tenant flap (agents vanished from its roster each minute).
			// Whether a room still exists is decided by the listing and by
			// the stream (watchRoom); here it only means there is no sync
			// plan to follow, so live-sync stays off for this room.
			slog.Debug("bnbloc bridge sync plan unavailable; live sync off for this room",
				"event", "bnbloc.bridge.sync_plan_unavailable", "room", room, "error", err)
			return
		}
		if mode == "mutagen" && dir != "" {
			if err := b.runSync(ctx, room, dir); err != nil {
				slog.Debug("bnbloc bridge sync ended",
					"event", "bnbloc.bridge.sync_ended", "room", room, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// syncPlan returns the room's sync mode and the local project directory to
// mirror. The directory is SyncDirFor(room) if provided, else the bound
// agent-session workspace directory. err is the room lookup's failure (a
// gone room surfaces as a 404/403 gatewayError).
func (b *Bridge) syncPlan(ctx context.Context, room string) (mode, dir string, err error) {
	raw, err := b.gatewayGET(ctx, "/v1/rooms/"+room)
	if err != nil {
		return "", "", err
	}
	var rm struct {
		SyncMode string `json:"syncMode"`
	}
	if json.Unmarshal(raw, &rm) != nil {
		return "", "", nil
	}
	if b.cfg.SyncDirFor != nil {
		return rm.SyncMode, b.cfg.SyncDirFor(room), nil
	}
	// Default: the workspace cwd of the room's most recently driven session,
	// fetched from that session's core.
	rs := b.latestRoomSession(room)
	if rs == nil {
		return rm.SyncMode, "", nil
	}
	return rm.SyncMode, b.workspaceDir(ctx, rs.core, rs.workspaceID, rs.sessionID), nil
}

// latestRoomSession returns the room's most recently driven bound session.
// Sessions are keyed per (room, core, provider), so a room can have several;
// live sync mirrors the one the room used last.
func (b *Bridge) latestRoomSession(room string) *roomSession {
	prefix := room + "\x00"
	b.mu.Lock()
	defer b.mu.Unlock()
	var best *roomSession
	var bestAt int64
	for key, rs := range b.sessions {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		at := rs.lastTurnStart()
		if best == nil || at > bestAt || (at == bestAt && rs.sessionID < best.sessionID) {
			best, bestAt = rs, at
		}
	}
	return best
}

// runSync dials the relay stream and runs one engine session until it ends.
func (b *Bridge) runSync(ctx context.Context, room, dir string) error {
	conn, err := syncpkg.DialRelayStream(b.cfg.RelayBaseURL, "sync:"+room, 15*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	engine, err := syncpkg.New(b.cfg.SyncEngine)
	if err != nil {
		return err
	}
	slog.Info("bnbloc bridge sync started",
		"event", "bnbloc.bridge.sync_started", "room", room, "engine", engine.Name(), "dir", dir)
	go func() { <-ctx.Done(); _ = engine.Stop() }()
	return engine.Start(ctx, syncpkg.SyncConfig{
		Dir:       dir,
		Conn:      conn,
		SelfID:    b.cfg.OwnerSubject,
		AuthToken: b.syncAuthToken(room),
		// PeerID is refined from the peer's HELLO; this is only the pre-HELLO
		// fallback for authority election.
		PeerID: "~peer:" + room,
		OnConflict: func(e syncpkg.ConflictEvent) {
			msg := "sync conflict on " + e.Path + " (" + e.Reason + ")"
			if e.ConflictCopy != "" {
				msg += "; peer version kept as " + e.ConflictCopy
			}
			_, _ = b.gatewayPOST(ctx, "/v1/rooms/"+room+"/chat", map[string]any{"text": msg, "atUnixMs": time.Now().UnixMilli()})
		},
	})
}

// syncAuthToken derives the per-room sync auth token as HMAC(SyncSecret, roomId).
// Both members' bridges hold the same deployment SyncSecret, so they compute the
// same token; a peer without the secret cannot pair on the room's sync stream.
// An empty SyncSecret yields an empty token (dev/single-host only).
func (b *Bridge) syncAuthToken(room string) string {
	if b.cfg.SyncSecret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(b.cfg.SyncSecret))
	_, _ = mac.Write([]byte(room))
	return hex.EncodeToString(mac.Sum(nil))
}

// workspaceDir returns the agent session's working directory on its bound
// core — the project directory to mirror for that room — through the core's
// optional SessionWorkdirReporter capability. A core without it (or a failed
// lookup) leaves the default sync directory unset.
func (b *Bridge) workspaceDir(ctx context.Context, coreName, workspaceID, sessionID string) string {
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return ""
	}
	reporter, ok := corepkg.As[corepkg.SessionWorkdirReporter](ctx, entry.provider, entry.endpoint, corepkg.CapabilitySessionWorkdir)
	if !ok {
		slog.Debug("bnbloc bridge default sync dir skipped: core reports no session workdir",
			"event", "bnbloc.bridge.sync_dir_skip", "core", coreName)
		return ""
	}
	dir, err := reporter.SessionWorkdir(ctx, entry.endpoint, workspaceID, sessionID)
	if err != nil {
		slog.Debug("bnbloc bridge session workdir lookup failed",
			"event", "bnbloc.bridge.sync_dir_skip", "core", coreName, "error", err)
		return ""
	}
	return strings.TrimSpace(dir)
}

// streamRoom holds one SSE subscription, reconnecting with backoff.
func (b *Bridge) streamRoom(ctx context.Context, room string) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := b.consumeRoom(ctx, room)
		if ctx.Err() != nil {
			return
		}
		if roomGone(err) {
			// Deleted, or the owner lost access: retrying cannot succeed.
			b.dropGoneRoom(room, err)
			return
		}
		if err != nil {
			// A room stream carries cancellation, so losing it degrades the worker to
			// lease-timeout aborts. Warn rather than hide it at debug level.
			slog.Warn("bnbloc bridge room stream ended",
				"event", "bnbloc.bridge.stream_ended", "room", room, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (b *Bridge) consumeRoom(ctx context.Context, room string) error {
	// Advertise the agents this bridge can drive in the room as ?agents=
	// (core/provider,…). The realtime service ties their liveness to this SSE
	// subscription, so members' composer pickers see real drivable agents
	// without the gateway reaching this machine (remote-safe), and they vanish
	// after a grace window when the bridge disconnects.
	adv := b.advertiseParam()
	sandboxed := b.sandboxedParam(ctx, adv)
	// Record what we advertise so refreshRoster only re-subscribes on a real
	// change, not on the first post-subscribe tick.
	b.rosterMu.Lock()
	b.lastRoster = adv + "|" + sandboxed
	b.rosterInit = true
	b.rosterMu.Unlock()
	streamURL := b.cfg.GatewayBaseURL + "/v1/rooms/" + room + "/events?presence=0&allChannels=1&replay=0"
	if adv != "" {
		streamURL += "&agents=" + url.QueryEscape(adv)
	}
	if sandboxed != "" {
		streamURL += "&sandboxed=" + url.QueryEscape(sandboxed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+b.gatewayAuth(ctx))
	resp, err := b.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("subscribe %s: %w: %s", room, &gatewayError{Method: http.MethodGet, URL: b.cfg.GatewayBaseURL + "/v1/rooms/" + room + "/events", Status: resp.StatusCode}, body)
	}
	// Bridge v2 never drives work from SSE — durable jobs carry the prompt. The
	// one thing worth listening for is a job leaving this bridge's hands: a member
	// pressing stop must halt the local turn now, not at the next lease renewal.
	rd := sse.NewReader(resp.Body)
	for {
		ev, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch ev.Name {
		case "agent_job_updated":
			b.handleJobUpdate(string(ev.Data))
		case "artifact_state":
			// A member interacted with a live artifact. Recorded for the
			// agent's .room/live.md; never drives a turn by itself (every
			// gesture spawning token-burning turns would be a runaway),
			// but the trail is there the next time the room talks to us.
			b.recordGesture(room, parseGesture(string(ev.Data)))
		}
	}
}

// liveGesture is one observed artifact interaction (from artifact_state).
type liveGesture struct {
	AtUnixMs int64
	Actor    string
	Artifact string
	State    string // compact JSON, bounded by maxLiveGestureBytes
}

const (
	maxLiveGestures     = 32 // per room, newest kept
	maxLiveGestureBytes = 2048
)

// recordGesture appends a member's live-artifact interaction to the room's
// trail (bounded ring). Malformed or oversize states are clamped, not dropped:
// the agent should still see THAT something happened.
func (b *Bridge) recordGesture(room string, g liveGesture) {
	if len(g.State) > maxLiveGestureBytes {
		g.State = g.State[:maxLiveGestureBytes] + "…(truncated)"
	}
	b.liveMu.Lock()
	defer b.liveMu.Unlock()
	if b.live == nil {
		b.live = make(map[string][]liveGesture)
	}
	trail := append(b.live[room], g)
	if len(trail) > maxLiveGestures {
		trail = trail[len(trail)-maxLiveGestures:]
	}
	b.live[room] = trail
}

// liveTrail returns a copy of the room's recent gestures (oldest first).
func (b *Bridge) liveTrail(room string) []liveGesture {
	b.liveMu.Lock()
	defer b.liveMu.Unlock()
	out := make([]liveGesture, len(b.live[room]))
	copy(out, b.live[room])
	return out
}

// jobUpdateEnvelope is the SSE frame realtime sends: the room event wrapper with
// the durable job as its payload. The job is NOT at the top level, so the
// payload must be unwrapped before reading a job id.
type jobUpdateEnvelope struct {
	Kind    string `json:"kind"`
	Payload struct {
		JobID  string `json:"jobId"`
		Status string `json:"status"`
	} `json:"payload"`
}

// parseGesture extracts the gesture trail entry from an artifact_state event.
func parseGesture(data string) liveGesture {
	var ev struct {
		AtUnixMs int64 `json:"atUnixMs"`
		Payload  struct {
			ArtifactID string          `json:"artifactId"`
			Actor      string          `json:"actor"`
			State      json.RawMessage `json:"state"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(data), &ev) != nil {
		return liveGesture{}
	}
	state := string(ev.Payload.State)
	if state == "" {
		state = "{}"
	}
	return liveGesture{
		AtUnixMs: ev.AtUnixMs,
		Actor:    ev.Payload.Actor,
		Artifact: ev.Payload.ArtifactID,
		State:    state,
	}
}

// handleJobUpdate stops a local turn the moment its job stops being ours. Cancel
// clears the lease server-side, so waiting for the next lease renewal would leave
// the agent burning the member's tokens on work they already stopped.
func (b *Bridge) handleJobUpdate(data string) {
	var ev jobUpdateEnvelope
	if json.Unmarshal([]byte(data), &ev) != nil || ev.Payload.JobID == "" {
		return
	}
	switch store.AgentJobStatus(ev.Payload.Status) {
	case store.AgentJobCancelled, store.AgentJobFailed, store.AgentJobTimedOut:
	default:
		return // still ours (or someone else's); nothing to stop
	}
	b.abortJob(ev.Payload.JobID, ev.Payload.Status)
}

var roomLocks sync.Map // "roomId\x00core\x00provider" -> *sync.Mutex

// driveClaim is the single execution entrypoint for a durable job. Drives are
// serialized per (room, core, provider, execution kind) so consecutive turns
// for one agent session queue instead of interleaving on the same core
// session. prepareExec (nil = run on the host) readies a sandboxed turn under
// that lock — space re-ensure + a fresh exec token — and its descriptor rides
// the session create / input.
func (b *Bridge) driveClaim(ctx context.Context, job jobRef, room, owner, core, provider, target, text string, attachmentIDs []string, opts corepkg.SessionOptions, prepareExec func(context.Context) (*corepkg.Execution, error), channelID string, beforeReflow func(*roomSession)) (driveResult, error) {
	jobID := job.ID
	key := driveSessionKey(room, core, provider, prepareExec != nil)
	lockAny, _ := roomLocks.LoadOrStore(key, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	opts.Execution = nil
	if prepareExec != nil {
		exec, err := prepareExec(ctx)
		if err != nil {
			b.postDriveFailed(ctx, jobID, room, owner, provider, "the agent sandbox could not be prepared: "+err.Error(), channelID)
			return driveResult{}, err
		}
		opts.Execution = exec
	}

	// Fresh sessions materialize attachments + build their prompt inside
	// ensureSession (workspace exists, session does not yet); a reused session
	// materializes there too and returns the written descriptors so the
	// follow-up prompt below can name them.
	promptFn := func(workspaceID string, st *roomlog.CtxState, atts []writtenAttachment) string {
		return b.drivePrompt(ctx, room, owner, core, provider, workspaceID, "", text, atts, st, true)
	}
	rs, err := b.ensureSession(ctx, job, room, owner, core, provider, target, channelID, attachmentIDs, promptFn, opts)
	if err != nil {
		b.postDriveFailed(ctx, jobID, room, owner, provider, cleanDriveError(err), channelID)
		return driveResult{}, err
	}
	// If the session was freshly created, the initial prompt already ran; only
	// send follow-up input for an existing session.
	if !rs.freshlyCreated {
		entry, _ := b.coreEntry(core)
		rs.roomSession.startTurn(TurnInfo{JobID: job.ID, RequesterUserID: job.RequesterUserID, ChannelID: channelID, MessageSeq: job.MessageSeq, Workspace: sessionWorkspace{b, rs.roomSession}})
		prompt := b.drivePrompt(ctx, room, owner, core, provider, rs.workspaceID, rs.sessionID, text, rs.written, &rs.roomSession.ctx, false)
		if err := b.sendInput(ctx, entry, rs.workspaceID, rs.sessionID, prompt, opts); err != nil {
			b.postDriveFailed(ctx, jobID, room, owner, provider, cleanDriveError(err), channelID)
			return driveResult{workspaceID: rs.workspaceID, sessionID: rs.sessionID}, err
		}
	}
	rs.roomSession.markTurn(deliverableTurn{StartMs: time.Now().UnixMilli(), JobID: job.ID, SourceSeq: job.MessageSeq})
	// Signal the room that the agent is working, so members see a live indicator
	// while codex runs; clear it once the turn drains. Best-effort presentation.
	b.postSessionState(ctx, room, rs.roomSession, "working")
	if beforeReflow != nil {
		beforeReflow(rs.roomSession)
	}
	// Wait for the driven turn to actually begin before reflowing. A freshly
	// created session (or a just-sent input) may not have registered its
	// activeTurnId yet; without this, reflow would observe a premature "idle",
	// drain only the prompt, and exit before the agent produces any reply — the
	// reply would then surface only on the next message's reflow.
	b.waitTurnStart(ctx, rs.roomSession)
	var usage turnUsage
	replied, err := b.reflow(ctx, jobID, room, rs.roomSession, channelID, &usage)
	b.postSessionState(ctx, room, rs.roomSession, "idle")
	return driveResult{workspaceID: rs.workspaceID, sessionID: rs.sessionID, replied: replied, usage: usage}, err
}

type ensureResult struct {
	*roomSession
	freshlyCreated bool
	// written is the set of attachments materialized into the workspace for this
	// turn, so a follow-up (reused-session) prompt can name them.
	written []writtenAttachment
}

// ensureSession binds a (room, provider) to a local workspace+session, creating
// them on the first message for that provider and reusing them afterward. Each
// provider in a room gets its own session, so codex and claude are independent
// conversations. A core can mint its own workspace id (roomId != workspaceId), so
// the binding is tracked here, not by shared id.
// promptFn composes the session's initial prompt once its workspace exists AND
// the turn's attachments are materialized: pull-capable cores materialize
// .room/ context first, so the prompt may depend on the workspace id, reports
// the materialized state through st, and names the written attachments.
type promptFn func(workspaceID string, st *roomlog.CtxState, atts []writtenAttachment) string

func (b *Bridge) ensureSession(ctx context.Context, job jobRef, room, owner, core, provider, target, channelID string, attachmentIDs []string, prompt promptFn, opts corepkg.SessionOptions) (*ensureResult, error) {
	entry, ok := b.coreEntry(core)
	if !ok {
		return nil, fmt.Errorf("unknown core %q", core)
	}
	if !entry.endpoint.OK() {
		// Configured but its daemon has not been found yet (it is resolved on
		// the next roster tick once it is up). Say so instead of failing on an
		// empty URL.
		return nil, fmt.Errorf("the %s core is not running on this machine yet", core)
	}
	key := driveSessionKey(room, core, provider, opts.Execution != nil)
	contentSum := job.content.contentSum()
	b.mu.Lock()
	existing := b.sessions[key]
	if existing != nil && existing.profileSum != contentSum {
		// The turn wears different content (another expert, a new version,
		// a changed skill pack): a new session, whose files the core (or
		// the fallback below) materializes from scratch.
		slog.Info("bnbloc bridge content changed, starting a new session",
			"event", "bnbloc.bridge.session_content_changed", "room", room, "core", core, "provider", provider, "session", existing.sessionID)
		b.unbindLocked(key)
		existing = nil
	}
	b.mu.Unlock()
	if existing != nil {
		if err := b.sessionReachable(ctx, entry, existing.workspaceID, existing.sessionID); err != nil {
			slog.Warn("bnbloc bridge rebinding stale session",
				"event", "bnbloc.bridge.session_rebind", "room", room, "core", core,
				"provider", provider, "session", existing.sessionID, "error", err)
			b.mu.Lock()
			if b.sessions[key] == existing {
				b.unbindLocked(key)
			}
			b.mu.Unlock()
		} else {
			// Reused session: materialize this turn's attachments into the
			// existing workspace BEFORE the caller composes the follow-up prompt,
			// so the prompt can name files that actually exist.
			written := b.materializeAttachments(ctx, core, room, existing.workspaceID, attachmentIDs)
			return &ensureResult{roomSession: existing, freshlyCreated: false, written: written}, nil
		}
	}
	if opts.Execution != nil {
		// The sandbox workdir outlives sessions: clear the previous
		// session's agent config before this one materializes its own.
		resetAgentConfig(opts.Execution.Sandbox.Workdir)
	}
	workspaceID, err := b.createWorkspace(ctx, entry, room, opts.Execution)
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	// Fresh session: materialize attachments into the new workspace BEFORE the
	// initial prompt runs (createSession below executes it), so the agent's very
	// first turn can open the files. This ordering is the whole fix — the old
	// code materialized after the fresh turn had already started.
	written := b.materializeAttachments(ctx, core, room, workspaceID, attachmentIDs)
	var st roomlog.CtxState
	initialPrompt := prompt(workspaceID, &st, written)
	// The profile the session wears: a core advertising agentProfile
	// receives the content's profile with the session contributors' parts
	// merged in; any other core gets the content as a prefix of the first
	// prompt (its skill files written under .agents/skills/ when it accepts
	// workspace files).
	var profile *corepkg.AgentProfile
	var hooks sessionHooks
	if corepkg.Supports(ctx, entry.provider, entry.endpoint, corepkg.CapabilityAgentProfile) {
		profile = job.content.profile()
		if profile == nil {
			profile = &corepkg.AgentProfile{}
		}
		hooks = b.contribute(ctx, SessionInfo{
			Key: key, TenantID: job.TenantID, RoomID: room, ChannelID: channelID, Owner: owner, Core: core, Provider: provider,
			Sandboxed: opts.Execution != nil, MCPTransports: b.targetTransports(core, target, provider),
			JobID: job.ID, RequesterUserID: job.RequesterUserID,
		}, profile)
		if profile.Empty() {
			profile = nil
		}
	} else if !job.content.empty() {
		written := b.writeFallbackSkills(ctx, core, workspaceID, job.content)
		initialPrompt = job.content.promptPrefix(written) + initialPrompt
	}
	rs := &roomSession{owner: owner, provider: provider, core: core, workspaceID: workspaceID, ctx: st, hooks: hooks, profileSum: contentSum}
	rs.startTurn(TurnInfo{JobID: job.ID, RequesterUserID: job.RequesterUserID, ChannelID: channelID, MessageSeq: job.MessageSeq, Workspace: sessionWorkspace{b, rs}})
	sessionID, err := b.createSession(ctx, entry, workspaceID, target, initialPrompt, opts, profile)
	if err != nil {
		rs.release()
		b.deleteWorkspace(ctx, entry, workspaceID)
		return nil, fmt.Errorf("create session: %w", err)
	}
	// Under mu: the session's workspace reader (a room tool call during
	// this first turn) reads it concurrently.
	rs.mu.Lock()
	rs.sessionID = sessionID
	rs.mu.Unlock()
	b.mu.Lock()
	b.sessions[key] = rs
	b.mu.Unlock()
	slog.Info("bnbloc bridge bound room to session",
		"event", "bnbloc.bridge.session_bound", "room", room, "core", core, "provider", provider, "workspace", workspaceID, "session", sessionID)
	return &ensureResult{roomSession: rs, freshlyCreated: true, written: written}, nil
}

// --- local agent core (via core.Provider) ---

// sessionKey names a bound session and its claim lock by room, core, and
// harness provider, so two cores' same-harness sessions in one room do not
// collide.
func sessionKey(room, core, provider string) string {
	return room + "\x00" + core + "\x00" + provider
}

// driveSessionKey keys a sandboxed agent's session apart from its host one:
// a session's workspace lives either on the host or under the sandbox
// workspaces root, so a change of execution kind (mode switch, sandbox
// ready/unavailable, own vs borrowed) starts a separate session and
// workspace instead of moving one. The "roomId\x00" prefix is kept, so
// forgetRoom prunes both.
func driveSessionKey(room, core, provider string, sandboxed bool) string {
	if sandboxed {
		return sessionKey(room, core, provider) + "\x00sandbox"
	}
	return sessionKey(room, core, provider)
}

// resolveCore picks the core to drive: the named core when this bridge runs
// it, or the default (first configured) core when the job names none. A named
// core this bridge does not run is NOT redirected to another core — that would
// answer with a different agent than the member addressed — so ok is false.
func (b *Bridge) resolveCore(name string) (string, bool) {
	if name == "" {
		return b.defaultCore, b.defaultCore != ""
	}
	_, ok := b.coreEntry(name)
	return name, ok
}

// resolveTarget derives the core-local agent target id for a harness/provider.
// Cores with structural target ids can resolve directly; others fall back to
// live target listing and provider-name matching.
func (b *Bridge) resolveTarget(ctx context.Context, coreName, provider string) string {
	entry, ok := b.coreEntry(coreName)
	if !ok {
		return provider
	}
	if resolver, ok := entry.provider.(corepkg.TargetResolver); ok {
		if target, ok := resolver.ResolveTarget(provider); ok && strings.TrimSpace(target) != "" {
			return target
		}
	}
	if !entry.endpoint.OK() {
		return provider
	}
	lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	targets, err := entry.provider.ListTargets(lctx, entry.endpoint)
	if err != nil {
		return provider
	}
	for _, t := range targets {
		if strings.EqualFold(t.Provider, provider) {
			return t.ID
		}
	}
	return provider
}

// providerDisabled reports whether the user turned off this (core, provider).
func (b *Bridge) providerDisabled(core, provider string) bool {
	if b.cfg.DisabledProviders == nil {
		return false
	}
	return b.cfg.DisabledProviders[core+"/"+provider]
}

// advertiseParam builds the ?agents= value: the "core/provider" pairs this
// bridge can drive, from each core's enabled targets as last listed by
// refreshTargets. It is the roster the realtime service exposes to composer
// pickers, so members discover drivable agents without the gateway reaching
// this machine. A core with no listed targets contributes nothing, and the
// whole param is empty when nothing resolves (the bridge still subscribes, it
// just advertises no roster this cycle).
func (b *Bridge) advertiseParam() string {
	seen := map[string]struct{}{}
	var pairs []string
	for _, name := range b.coreNames {
		for _, t := range b.cachedTargets(name) {
			if b.providerDisabled(name, t.Provider) {
				continue
			}
			pair := name + "/" + t.Provider
			if _, ok := seen[pair]; ok {
				continue
			}
			seen[pair] = struct{}{}
			pairs = append(pairs, pair)
		}
	}
	return strings.Join(pairs, ",")
}

// sandboxedParam is the ?sandboxed= value: the advertised "core/provider"
// pairs whose borrowed drives this bridge runs in the agent sandbox (the
// room roster's shield badge).
func (b *Bridge) sandboxedParam(ctx context.Context, adv string) string {
	if adv == "" {
		return ""
	}
	var out []string
	supports := map[string]bool{}
	for _, pair := range strings.Split(adv, ",") {
		core, _, _ := strings.Cut(pair, "/")
		ok, seen := supports[core]
		if !seen {
			ok = b.sandboxedAgent(ctx, core)
			supports[core] = ok
		}
		if ok {
			out = append(out, pair)
		}
	}
	return strings.Join(out, ",")
}

// rosterKey is what a room subscription advertises (agents + sandboxed); a
// change re-subscribes the room streams.
func (b *Bridge) rosterKey(ctx context.Context) string {
	adv := b.advertiseParam()
	return adv + "|" + b.sandboxedParam(ctx, adv)
}

// cacheTargets / cachedTargets remember the last successful ListTargets per core
// so a transient discovery/list miss does not blank the advertised roster (and
// churn room streams). Guarded by rosterMu.
func (b *Bridge) cacheTargets(name string, ts []corepkg.Target) {
	b.rosterMu.Lock()
	if b.targetCache == nil {
		b.targetCache = map[string][]corepkg.Target{}
	}
	b.targetCache[name] = ts
	b.rosterMu.Unlock()
}

func (b *Bridge) cachedTargets(name string) []corepkg.Target {
	b.rosterMu.Lock()
	defer b.rosterMu.Unlock()
	return b.targetCache[name]
}

// coreEntry returns a snapshot of a core's provider+endpoint under the lock, so
// readers never race refreshRoster updating b.cores. The returned value is a
// copy; callers use it read-only.
func (b *Bridge) coreEntry(name string) (coreEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.cores[name]
	return e, ok
}

// createWorkspace creates the core workspace of a new session. A sandboxed
// session's workspace is created IN its sandbox workdir (capability
// sandboxExec), so the .room/ context and attachments written before the
// first turn land where the confined CLI and the guest see them.
func (b *Bridge) createWorkspace(ctx context.Context, entry coreEntry, room string, exec *corepkg.Execution) (string, error) {
	var create func() (string, error)
	if exec != nil {
		creator, ok := corepkg.As[corepkg.WorkdirWorkspaceCreator](ctx, entry.provider, entry.endpoint, corepkg.CapabilitySandboxExec)
		if !ok {
			return "", errors.New("the core no longer supports sandbox execution")
		}
		create = func() (string, error) { return creator.CreateWorkspaceAt(ctx, entry.endpoint, exec.Sandbox.Workdir) }
	} else {
		create = func() (string, error) { return entry.provider.CreateWorkspace(ctx, entry.endpoint) }
	}
	var id string
	err := retryCore(ctx, false, func() error {
		var e error
		id, e = create()
		return e
	})
	return id, err
}

// deleteWorkspace best-effort removes a workspace via the core's optional
// WorkspaceDeleter capability. Cores without it (or a delete that errors) just
// log — orphan cleanup must never fail a drive.
func (b *Bridge) deleteWorkspace(ctx context.Context, entry coreEntry, workspaceID string) {
	deleter, ok := corepkg.As[corepkg.WorkspaceDeleter](ctx, entry.provider, entry.endpoint, corepkg.CapabilityDeleteWorkspace)
	if !ok {
		return
	}
	if err := deleter.DeleteWorkspace(ctx, entry.endpoint, workspaceID); err != nil {
		slog.Debug("bnbloc bridge orphan workspace cleanup failed",
			"event", "bnbloc.bridge.workspace_cleanup_failed", "workspace", workspaceID, "error", err)
	}
}

func (b *Bridge) createSession(ctx context.Context, entry coreEntry, workspaceID, target, prompt string, opts corepkg.SessionOptions, profile *corepkg.AgentProfile) (string, error) {
	var sessionID string
	err := retryCore(ctx, false, func() error {
		res, e := entry.provider.CreateSession(ctx, entry.endpoint, corepkg.SessionRequest{
			WorkspaceID: workspaceID,
			TargetID:    target,
			Prompt:      prompt,
			Options:     opts,
			Profile:     profile,
		})
		if e != nil {
			return e
		}
		sessionID = res.SessionID
		return nil
	})
	return sessionID, err
}

func (b *Bridge) sendInput(ctx context.Context, entry coreEntry, workspaceID, sessionID, text string, opts corepkg.SessionOptions) error {
	return retryCore(ctx, false, func() error {
		return entry.provider.SendInput(ctx, entry.endpoint, workspaceID, sessionID, text, opts)
	})
}

// retryCore runs a core call with a short bounded retry (a few hundred ms) so a
// drive that races a daemon restart succeeds instead of failing the whole turn,
// while a hard failure (disconnected runtime, auth error) still surfaces
// promptly. Whether a failure may be repeated is core.Retryable's call:
// creating a session and sending input start agent work, so they are repeated
// only when the request provably never took effect — retrying an ambiguous
// failure (reset mid-response, 5xx) would run the member's prompt twice.
func retryCore(ctx context.Context, idempotent bool, fn func() error) error {
	delays := []time.Duration{300 * time.Millisecond, 700 * time.Millisecond}
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil || !corepkg.Retryable(err, idempotent) || attempt >= len(delays) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delays[attempt]):
		}
	}
}

// sessionReachable reports whether a bound session still exists on its core: it
// probes the turn status and treats a successful poll (idle OR busy) as live.
// Only a poll error — the core restarted, or the session was dropped — means
// the binding is stale and must be rebound. It deliberately does NOT wait for
// the turn to finish: a long-running turn is a valid, reusable session, and the
// core queues a follow-up behind it.
func (b *Bridge) sessionReachable(ctx context.Context, entry coreEntry, workspaceID, sessionID string) error {
	pollCtx, cancel := context.WithTimeout(ctx, corePollTimeout)
	defer cancel()
	_, err := entry.provider.PollTurn(pollCtx, entry.endpoint, workspaceID, sessionID)
	return err
}

// --- reflow: local messages -> bnbloc room ---

// reflow polls the session's messages after the last reflowed cursor and pushes
// new assistant replies into the room until the turn is idle and drained. It
// returns whether a visible assistant reply was posted, and folds each consumed
// reply's reported metrics into usage (nil = not tracked).
func (b *Bridge) reflow(ctx context.Context, jobID, room string, rs *roomSession, channelID string, usage *turnUsage) (bool, error) {
	// idleDeadline is a sliding "no activity" window: the turn may run for a
	// long time (codex can work for minutes), so a fixed cap would truncate a
	// live reply. Each sign of progress — a new message, or a still-busy turn —
	// pushes the window forward; only a genuine stall (no output and no active
	// turn) for the whole window ends the reflow. hardDeadline bounds the total
	// so a wedged session can never pin the goroutine forever.
	const idleWindow = 120 * time.Second
	const maxTurn = 30 * time.Minute
	idleDeadline := time.Now().Add(idleWindow)
	hardDeadline := time.Now().Add(maxTurn)
	posted := false // any user-visible assistant reply reached the room this turn
	// Deliverables surface while the turn runs, not only after it drains: a
	// member watching Docs sees the agent's files appear as they are written.
	// Throttled because reflow polls every few seconds and the publish pass
	// costs one manifest fetch plus reads of only the changed files.
	lastPublish := time.Now()
	const publishEvery = 10 * time.Second
	// consecutive poll failures; tolerate a short blip (daemon restart, network
	// hiccup) instead of ending the turn — the reply would otherwise be lost.
	fails := 0
	const maxFails = 8
	for {
		entry, ok := b.coreEntry(rs.core)
		if !ok {
			return posted, fmt.Errorf("unknown core %q", rs.core)
		}
		progressed := false
		pollCtx, cancelPoll := context.WithTimeout(ctx, corePollTimeout)
		msgs, latest, err := entry.provider.Messages(pollCtx, entry.endpoint, rs.workspaceID, rs.sessionID, rs.lastVersion)
		cancelPoll()
		var postErr error
		if err == nil {
			drained := true
			for _, m := range msgs {
				if m.Version <= rs.lastVersion {
					continue
				}
				if strings.EqualFold(m.Role, "assistant") && m.UserVisible && m.Completed &&
					strings.TrimSpace(m.Text) != "" {
					if perr := b.postRoomMessage(ctx, room, rs, m, channelID); perr != nil {
						if !permanentGatewayError(perr) {
							// The reply did not reach the room. Keep the cursor
							// BEFORE it so the next poll re-posts it (activity
							// dedupes by its stable seq if the post did land).
							postErr = perr
							drained = false
							break
						}
						// The gateway will never accept this message (4xx);
						// retrying cannot help, so it is logged and skipped.
						slog.Warn("bnbloc bridge reply rejected by gateway, skipping",
							"event", "bnbloc.bridge.post_rejected", "room", room, "message", m.MessageID, "error", perr)
						usage.add(m.Metrics) // consumed: the turn still used it
					} else {
						posted = true
						progressed = true
						usage.add(m.Metrics)
						usage.noteReply(m)
					}
				}
				if m.Version > rs.lastVersion {
					rs.lastVersion = m.Version
				}
			}
			if drained && latest > rs.lastVersion {
				rs.lastVersion = latest
			}
		}
		if time.Since(lastPublish) >= publishEvery {
			lastPublish = time.Now()
			b.publishDeliverables(ctx, room, channelID, rs, rs.lastTurn())
		}
		pollCtx, cancelPoll = context.WithTimeout(ctx, corePollTimeout)
		ts, tsErr := entry.provider.PollTurn(pollCtx, entry.endpoint, rs.workspaceID, rs.sessionID)
		cancelPoll()
		switch {
		case err != nil || tsErr != nil || postErr != nil:
			// A poll (or a reply post) failed. Tolerate a bounded run of
			// consecutive failures so a daemon restart or a gateway blip
			// mid-turn does not drop the reply; give up only when they persist.
			fails++
			if fails >= maxFails {
				slog.Warn("bnbloc bridge reflow giving up after repeated errors",
					"event", "bnbloc.bridge.reflow_poll_failed", "room", room,
					"core", rs.core, "session", rs.sessionID, "messagesErr", err, "turnErr", tsErr, "postErr", postErr)
				return posted, firstError(err, tsErr, postErr)
			}
		case ts.Idle:
			// The turn is done and drained. If it failed and produced no visible
			// reply, surface the error so the member sees why nothing came back
			// instead of a silent no-op (mirrors postBorrowDenied).
			if ts.Failed && !posted {
				b.postTurnFailed(ctx, jobID, room, rs, ts.FailMessage, channelID)
				return false, fmt.Errorf("turn failed: %s", ts.FailMessage)
			}
			return posted, nil
		default:
			fails = 0         // healthy poll of an active turn
			progressed = true // a still-running turn is progress
		}
		// Any progress (a posted reply or a still-active turn) slides the idle
		// window forward, so a long but live turn is never truncated.
		if progressed {
			idleDeadline = time.Now().Add(idleWindow)
		}
		if time.Now().After(idleDeadline) || time.Now().After(hardDeadline) {
			return posted, fmt.Errorf("reflow timeout")
		}
		select {
		case <-ctx.Done():
			return posted, ctx.Err()
		// Poll briskly so each completed assistant message reaches the room
		// promptly (per-message "streaming"). The core exposes no token-level
		// stream — only completed messages via REST — so sub-message streaming is
		// not possible here; this is the finest granularity available.
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (b *Bridge) isIdle(ctx context.Context, coreName, workspaceID, sessionID string) (bool, error) {
	entry, ok := b.coreEntry(coreName)
	if !ok {
		return false, fmt.Errorf("unknown core %q", coreName)
	}
	pollCtx, cancel := context.WithTimeout(ctx, corePollTimeout)
	defer cancel()
	ts, err := entry.provider.PollTurn(pollCtx, entry.endpoint, workspaceID, sessionID)
	if err != nil {
		return false, err
	}
	return ts.Idle, nil
}

// waitTurnStart briefly waits for a just-driven session to enter an active turn
// (activeTurnId set), so the subsequent reflow's drain-until-idle loop does not
// exit on a premature "idle" before the agent has begun producing output. It is
// best-effort: a turn that completes faster than the poll interval is caught by
// reflow's own message drain, so a short deadline is enough.
func (b *Bridge) waitTurnStart(ctx context.Context, rs *roomSession) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		idle, err := b.isIdle(ctx, rs.core, rs.workspaceID, rs.sessionID)
		if err == nil && !idle {
			return // turn is active; reflow can now drain until it goes idle
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// postRoomMessage pushes one assistant reply into the room in the exact wire
// shape the web client already renders. The activity seq is the message's
// stable RoomSeq, so re-posting after a failed attempt deduplicates instead of
// duplicating. The error is returned so reflow can retry instead of losing it.
func (b *Bridge) postRoomMessage(ctx context.Context, room string, rs *roomSession, m corepkg.Message, channelID string) error {
	payload := map[string]any{
		"messageId":   m.MessageID,
		"role":        "assistant",
		"text":        m.Text,
		"ownerUserId": firstNonEmpty(rs.owner, b.cfg.OwnerSubject),
		// The agent that produced this reply, so the feed can label it
		// (e.g. "codex") and distinguish multiple agents in one room.
		"provider": rs.provider,
	}
	// Turn observability rides the reply payload, additively: a core that
	// reports thinking/metrics (claude via stream-json) carries them into the
	// room record; a core that reports none leaves them absent, so older turns
	// and metric-less harnesses render with no footer.
	if m.Thinking != "" {
		payload["thinking"] = m.Thinking
	}
	if len(m.Metrics) > 0 {
		payload["metrics"] = m.Metrics
	}
	body := map[string]any{
		"workspaceId":    room,
		"agentSessionId": rs.sessionID,
		"channelId":      channelID,
		"messages": []map[string]any{{
			"seq":             m.RoomSeq(),
			"payload":         payload,
			"createdAtUnixMs": m.CreatedAtMs,
		}},
	}
	if _, err := b.gatewayPOST(ctx, "/v1/rooms/"+room+"/agent-activity/messages", body); err != nil {
		slog.Warn("bnbloc bridge post message failed",
			"event", "bnbloc.bridge.post_failed", "room", room, "error", err)
		return err
	}
	return nil
}

// borrowTarget builds the shared-agent identity the authority is keyed by. It
// is core-qualified (shared-agent:<core>:<provider>) so the same harness under
// two cores is authorized independently — the owner controls exactly which
// core's agent is borrowable. The authority treats the whole "<core>:<provider>"
// suffix as an opaque grant discriminator. A colon (not a slash) separates core
// and provider so the id stays a single URL path segment; core names carry no
// colon, and a provider that itself contains ':' (e.g. "acp:hermes") is
// unambiguous as the remainder after the core. An empty core falls back to the
// bare provider (legacy/coreless), preserving older single-core authorizations.
func borrowTarget(coreName, provider string) string {
	if strings.TrimSpace(coreName) == "" {
		return "shared-agent:" + provider
	}
	return "shared-agent:" + coreName + ":" + provider
}

// borrowDenyUnavailable is the deny reason when the authority could not be
// asked (network/server error): the borrow fails closed.
const borrowDenyUnavailable = "authorization_unavailable"

// borrowReservation is the authority's answer for one borrowed run.
type borrowReservation struct {
	// ID is the run's reservation ("" when nothing was reserved).
	ID string
	// DenyReason is set when the run may not be driven.
	DenyReason string
	// DefaultModel is a platform agent's default model, chosen by its host
	// ("" = none): the turn's model when the job named none.
	DefaultModel string
}

// authorizeBorrow decides whether a borrowed job may be driven: the run's
// reservation, or a deny reason.
//
// When this bridge acts AS the agent's owner it reserves the run with the
// shareagent authority (reserveBorrow): the reservation is the authorization
// and counts against the quota at once. A bridge driving another owner's
// agent (single-host serve-all testing) has no identity the authority accepts
// a reservation from (only the owner may reserve or record, so a member can
// never burn another's quota); it falls back to the read-only authorization
// check and the run is not accounted — serve-all is never a deployment mode.
//
// sandboxed is this drive's plan (it runs in the owner's agent sandbox): a
// platform agent's reservation is refused with shareagent.DenySandboxRequired
// for a drive that is not.
func (b *Bridge) authorizeBorrow(ctx context.Context, job bridgeJob, owner, coreName string, sandboxed bool) borrowReservation {
	if strings.TrimSpace(owner) != strings.TrimSpace(b.cfg.OwnerSubject) {
		slog.Debug("bnbloc bridge borrow not reserved: bridge does not act as the owner",
			"event", "bnbloc.bridge.borrow_reserve_skipped", "room", job.RoomID, "owner", owner, "bridgeOwner", b.cfg.OwnerSubject)
		if b.borrowAuthorized(ctx, job.RoomID, owner, coreName, job.Provider) {
			return borrowReservation{}
		}
		return borrowReservation{DenyReason: "owner_not_authorized"}
	}
	return b.reserveBorrow(ctx, job.RoomID, owner, coreName, job.Provider, job.RequesterUserID, job.Model, job.ChannelID, sandboxed)
}

// borrowAuthorized reports whether the agent's owner has a live room
// authorization allowing this (core, provider) to be borrowed (the read-only
// check, see authorizeBorrow). It fails closed on any error.
func (b *Bridge) borrowAuthorized(ctx context.Context, room, owner, coreName, provider string) bool {
	target := borrowTarget(coreName, provider)
	path := "/v1/rooms/" + room + "/shared-agents/" + url.PathEscape(target) +
		"/authorization?owner=" + url.QueryEscape(owner)
	raw, err := b.gatewayGET(ctx, path)
	if err != nil {
		slog.Warn("bnbloc bridge borrow authz check failed",
			"event", "bnbloc.bridge.borrow_authz_failed", "room", room, "owner", owner, "core", coreName, "provider", provider, "error", err)
		return false
	}
	var parsed struct {
		Authorized bool `json:"authorized"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return false
	}
	return parsed.Authorized
}

// reserveBorrow reserves one borrowed run with the shareagent authority (the
// owner's room authorization; R8). It returns the reservation (with a
// platform agent's default model), or the authority's deny reason
// (owner_not_authorized, quota_exhausted, cost_exhausted,
// org_quota_exhausted, platform_agent_unavailable, sandbox_required) —
// borrowDenyUnavailable when the authority could not be asked. It fails
// closed: no reservation, no drive. sandboxed reports this drive's plan; a
// reservation that answers sandboxRequired for a drive that is not sandboxed
// is refused here as sandbox_required too (and settled at once, empty, so it
// does not linger).
func (b *Bridge) reserveBorrow(ctx context.Context, room, owner, coreName, provider, borrower, model, channelID string, sandboxed bool) borrowReservation {
	target := borrowTarget(coreName, provider)
	path := "/v1/rooms/" + room + "/shared-agents/" + url.PathEscape(target) +
		"/authorization/reserve?owner=" + url.QueryEscape(owner)
	raw, err := b.gatewayPOST(ctx, path, map[string]any{"borrower": borrower, "model": strings.TrimSpace(model), "channelId": channelID, "sandboxed": sandboxed})
	var parsed struct {
		Reserved        bool   `json:"reserved"`
		ReservationID   string `json:"reservationId"`
		DenyReason      string `json:"denyReason"`
		SandboxRequired bool   `json:"sandboxRequired"`
		DefaultModel    string `json:"defaultModel"`
	}
	var gwErr *gatewayError
	switch {
	case err == nil:
		_ = json.Unmarshal(raw, &parsed)
	case errors.As(err, &gwErr) && gwErr.Status == http.StatusForbidden:
		_ = json.Unmarshal(gwErr.Body, &parsed)
	}
	if parsed.Reserved && parsed.ReservationID != "" {
		if parsed.SandboxRequired && !sandboxed {
			settleCtx := b.driveCtx
			if settleCtx == nil {
				settleCtx = context.WithoutCancel(ctx)
			}
			go b.settleBorrow(settleCtx, room, owner, coreName, provider, parsed.ReservationID, nil)
			return borrowReservation{DenyReason: shareagent.DenySandboxRequired}
		}
		return borrowReservation{ID: parsed.ReservationID, DefaultModel: strings.TrimSpace(parsed.DefaultModel)}
	}
	if parsed.DenyReason != "" {
		return borrowReservation{DenyReason: parsed.DenyReason}
	}
	slog.Warn("bnbloc bridge borrow reservation failed",
		"event", "bnbloc.bridge.borrow_reserve_failed", "room", room, "owner", owner, "core", coreName, "provider", provider, "error", err)
	return borrowReservation{DenyReason: borrowDenyUnavailable}
}

// settleBorrow settles a reserved borrowed run with the turn's reported usage
// (turnUsage.recordFields: model, tokens, cost — each only when known).
// Best-effort: the run already counted at reserve; a lost settle only leaves
// it without usage (the reservation expires).
func (b *Bridge) settleBorrow(ctx context.Context, room, owner, coreName, provider, reservationID string, usage map[string]any) {
	target := borrowTarget(coreName, provider)
	path := "/v1/rooms/" + room + "/shared-agents/" + url.PathEscape(target) +
		"/authorization/record?owner=" + url.QueryEscape(owner)
	body := map[string]any{"reservationId": reservationID}
	for k, v := range usage {
		body[k] = v
	}
	if _, err := b.gatewayPOST(ctx, path, body); err != nil {
		slog.Warn("bnbloc bridge borrow settle failed",
			"event", "bnbloc.bridge.borrow_settle_failed", "room", room, "owner", owner, "core", coreName, "provider", provider, "reservation", reservationID, "error", err)
	}
}

// Bridge notices — a refused borrow, a failed turn, an unreachable agent — are
// synthetic assistant rows that tell the member why nothing came back. They
// must never share an activity cursor with a real agent session: activity
// deduplicates replies per (room, agentSessionId) by seq, so a notice posted
// under the real session with a wall-clock seq (~1.8e12) advanced that
// session's cursor past every later real reply, which were then silently
// dropped as replays. Each notice therefore posts under its job's own notice
// session (wire.JobNoticeSessionID, the namespace activity's job sweeper also
// uses) at wire.JobNoticeSeq: one notice per job, idempotent on retry.

// noticeSessionID is the activity session a job's notice is recorded under
// (wire.JobNoticeSessionID, shared with activity's job sweeper). A notice
// without a job (never expected from the durable queue) still gets a session of
// its own rather than an empty key shared by everything.
func noticeSessionID(jobID string) string {
	if id := strings.TrimSpace(jobID); id != "" {
		return wire.JobNoticeSessionID(id)
	}
	return "notice-" + corepkg.UUIDv4()
}

// postNotice records one notice row for a job in the room feed.
func (b *Bridge) postNotice(ctx context.Context, jobID, room, channelID, kind string, payload map[string]any) {
	session := noticeSessionID(jobID)
	payload["messageId"] = kind + "-" + session
	payload["role"] = "assistant"
	body := map[string]any{
		"workspaceId":    room,
		"agentSessionId": session,
		"channelId":      channelID,
		"messages": []map[string]any{{
			"seq":             wire.JobNoticeSeq,
			"payload":         payload,
			"createdAtUnixMs": time.Now().UnixMilli(),
		}},
	}
	if _, err := b.gatewayPOST(ctx, "/v1/rooms/"+room+"/agent-activity/messages", body); err != nil {
		slog.Warn("bnbloc bridge post notice failed",
			"event", "bnbloc.bridge.post_failed", "room", room, "kind", kind, "error", err)
	}
}

// postBorrowDenied surfaces a refused borrow in the room feed, attributed to the
// requested agent handle, so the borrower sees why nothing ran instead of a
// silent no-op. The denial is visible, matching the design's accountable model.
func (b *Bridge) postBorrowDenied(ctx context.Context, jobID, room, owner, provider, channelID, denyReason string) {
	b.postNotice(ctx, jobID, room, channelID, "borrow-denied", map[string]any{
		"text":        borrowDeniedText(owner, provider, denyReason),
		"ownerUserId": owner,
		"provider":    provider,
		"denyReason":  denyReason,
	})
}

// borrowDeniedText explains a refused borrow to the borrower.
func borrowDeniedText(owner, provider, denyReason string) string {
	switch denyReason {
	case "quota_exhausted":
		return owner + "'s " + provider + " has used all the runs authorized in this room. Ask " + owner + " for more runs."
	case "cost_exhausted":
		return owner + "'s " + provider + " has used its whole cost allowance in this room."
	case "org_quota_exhausted":
		return "The organization's borrow quota is used up, so " + owner + "'s " + provider + " cannot be borrowed right now."
	case borrowDenyUnavailable:
		return "The borrow authorization for " + owner + "'s " + provider + " could not be checked. Try again shortly."
	case shareagent.DenyPlatformAgentUnavailable:
		return provider + " (hosted by " + owner + ") is no longer offered as a platform agent here."
	default:
		return owner + " has not authorized borrowing their " + provider + " in this room."
	}
}

// postTurnFailed surfaces a failed agent turn in the room feed, attributed to
// the driven agent. detail is the provider's error message (e.g. a missing API
// key); a generic line is used when it is empty.
func (b *Bridge) postTurnFailed(ctx context.Context, jobID, room string, rs *roomSession, detail string, channelID string) {
	text := "The agent could not complete this request"
	if d := strings.TrimSpace(detail); d != "" {
		text += ": " + d
	}
	b.postNotice(ctx, jobID, room, channelID, "turn-failed", map[string]any{
		"text":        text,
		"ownerUserId": firstNonEmpty(rs.owner, b.cfg.OwnerSubject),
		"provider":    rs.provider,
		"failReason":  "turn_failed",
	})
}

// postDriveFailed surfaces a failure to *start or send to* an agent (before any
// turn runs). detail is the cleaned core error (see cleanDriveError).
func (b *Bridge) postDriveFailed(ctx context.Context, jobID, room, owner, provider, detail, channelID string) {
	text := "The " + provider + " agent could not be reached"
	if d := strings.TrimSpace(detail); d != "" {
		text += ": " + d
	}
	b.postNotice(ctx, jobID, room, channelID, "drive-failed", map[string]any{
		"text":        text,
		"ownerUserId": firstNonEmpty(owner, b.cfg.OwnerSubject),
		"provider":    provider,
		"failReason":  "drive_failed",
	})
}

// cleanDriveError turns a core REST error into a short, member-readable reason.
// Core errors carry a JSON body (see core/rest.go) whose developerMessage holds
// the actionable cause (e.g. "agent runtime session is disconnected"); extract
// it when present, else fall back to a bounded tail of the raw error.
func cleanDriveError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		var parsed struct {
			Error struct {
				DeveloperMessage string `json:"developerMessage"`
				Reason           string `json:"reason"`
				Code             string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(raw[i:]), &parsed) == nil {
			if m := firstNonEmpty(
				strings.TrimSpace(parsed.Error.DeveloperMessage),
				strings.TrimSpace(parsed.Error.Reason),
				strings.TrimSpace(parsed.Error.Code),
			); m != "" {
				// developerMessage may carry newlines; keep the first line.
				if nl := strings.IndexByte(m, '\n'); nl >= 0 {
					m = m[:nl]
				}
				return m
			}
		}
	}
	return corepkg.Truncate([]byte(raw), 200)
}

// sameUserID compares two user ids case-insensitively (dev identities vary case).
func sameUserID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// serveRecipient reports whether this bridge should drive a chat addressed to
// recipientOwner's agent. In production a bridge serves only its own owner's
// agents; ServeAllOwners (single-host dev) serves any member from the one local
// core. An empty recipientOwner (legacy/self) is always served.
func serveRecipient(serveAll bool, recipientOwner, bridgeOwner string) bool {
	if serveAll || recipientOwner == "" {
		return true
	}
	return sameUserID(recipientOwner, bridgeOwner)
}

// isBorrow reports whether a chat is a borrow: its author is a real, different
// user than the agent's owner. A self-addressed chat (author == owner) or an
// unattributed one is not a borrow and needs no authorization.
func isBorrow(author, owner string) bool {
	a := strings.TrimSpace(author)
	return a != "" && !sameUserID(a, owner)
}

// firstNonEmpty returns the first non-empty string, else "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstPositive returns the first value > 0, else the last (the default).
func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	if len(vals) == 0 {
		return 0
	}
	return vals[len(vals)-1]
}

// postSessionState reports the agent's working/idle phase to the room so members
// see a live "agent is working…" indicator. The activity store keeps only the
// highest messageVersion per session, so the version must increase across bridge
// restarts too — a per-run counter starting at 0 would be rejected as stale
// after a restart (leaving the indicator stuck). Use the wall-clock millisecond
// timestamp, which is monotonic enough here and always beats a prior run.
func (b *Bridge) postSessionState(ctx context.Context, room string, rs *roomSession, phase string) {
	now := time.Now().UnixMilli()
	rs.mu.Lock()
	if now <= rs.stateVersion {
		now = rs.stateVersion + 1 // guarantee strictly increasing within a run
	}
	rs.stateVersion = now
	rs.mu.Unlock()
	body := map[string]any{
		"workspaceId":    room,
		"agentSessionId": rs.sessionID,
		// The agent's owner (matches how replies are attributed), so the
		// indicator lines up with a borrowed agent's <owner>/<provider> label.
		"ownerUserId": firstNonEmpty(rs.owner, b.cfg.OwnerSubject),
		// provider + core identify WHICH agent this state is for, so a room with
		// several agents running at once can show a per-agent indicator instead
		// of one ambiguous "working" bubble.
		"provider":         rs.provider,
		"core":             rs.core,
		"state":            map[string]any{"phase": phase},
		"messageVersion":   now,
		"occurredAtUnixMs": now,
	}
	if _, err := b.gatewayPOST(ctx, "/v1/rooms/"+room+"/agent-activity/session-state", body); err != nil {
		slog.Debug("bnbloc bridge post session-state failed",
			"event", "bnbloc.bridge.state_failed", "room", room, "error", err)
	}
}

// --- HTTP helpers ---

func (b *Bridge) gatewayGET(ctx context.Context, path string) ([]byte, error) {
	return b.do(ctx, http.MethodGet, b.cfg.GatewayBaseURL+path, b.gatewayAuth(ctx), nil)
}

// gatewayGETAttachment fetches an attachment's bytes plus its content-type and
// original filename (from the download response's Content-Type /
// Content-Disposition headers), so the bridge can write it with a real
// extension and name it in the prompt without a separate metadata call.
func (b *Bridge) gatewayGETAttachment(ctx context.Context, path string) (data []byte, contentType, filename string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.GatewayBaseURL+path, nil)
	if err != nil {
		return nil, "", "", err
	}
	if tok := b.gatewayAuth(ctx); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	fn := filenameFromContentDisposition(resp.Header.Get("Content-Disposition"))
	return raw, ct, fn, nil
}

// filenameFromContentDisposition extracts the filename from a
// `inline; filename="x.png"` header; empty when absent.
func filenameFromContentDisposition(cd string) string {
	const marker = `filename="`
	i := strings.Index(cd, marker)
	if i < 0 {
		return ""
	}
	rest := cd[i+len(marker):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}
func (b *Bridge) gatewayPOST(ctx context.Context, path string, body any) ([]byte, error) {
	return b.do(ctx, http.MethodPost, b.cfg.GatewayBaseURL+path, b.gatewayAuth(ctx), body)
}

// gatewayAuth returns the bearer for gateway calls: a static token if configured,
// otherwise a cached owner session token minted via login (refreshed every 30
// min). The owner token carries the owner's room memberships, which authorize
// room SSE subscription and agent-activity posts.
func (b *Bridge) gatewayAuth(ctx context.Context) string {
	if b.cfg.GatewayToken != "" {
		return b.cfg.GatewayToken
	}
	b.tokMu.Lock()
	defer b.tokMu.Unlock()
	if b.ownerTok != "" && time.Since(b.ownerTokAt) < 30*time.Minute {
		return b.ownerTok
	}
	// Mint the owner session from a refresh token when one was handed over;
	// the dev login (subject as the IdP token) only works on development
	// gateways and is refused by production ones.
	//
	// The handed-over token belongs to the member's client (the desktop web
	// session, or whoever configured BNBLOC_GATEWAY_REFRESH_TOKEN), which
	// keeps rotating it. Rotating it here too would present a rotated-away
	// token on one side and trip the gateway's reuse detection, signing the
	// member out. So the bridge FORKS once — a new refresh family of its own
	// for the same sign-in — and from then on rotates only its own token. The
	// handed-over token is never presented again after a successful fork.
	path, payload := "/v1/auth/login", map[string]string{"idpToken": b.cfg.OwnerSubject, "client": "bridge"}
	switch handed := strings.TrimSpace(b.cfg.RefreshToken); {
	case b.refreshTok != "":
		path, payload = "/v1/auth/refresh", map[string]string{"refreshToken": b.refreshTok}
	case handed != "" && !b.forked:
		path, payload = "/v1/auth/refresh/fork", map[string]string{"refreshToken": handed, "client": "bridge"}
	case handed != "":
		// Our own family was revoked (the member signed this bridge out):
		// stay signed out rather than replaying the stale handed-over token.
		return b.ownerTok
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.GatewayBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return b.ownerTok
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return b.ownerTok
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return b.ownerTok
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("bnbloc bridge could not mint an owner session", "event", "bnbloc.bridge.session_failed", "path", path, "status", resp.StatusCode)
		if path == "/v1/auth/refresh" && resp.StatusCode == http.StatusUnauthorized {
			// Our family is revoked or expired; a retry can never succeed.
			b.refreshTok = ""
		}
		return b.ownerTok
	}
	var out struct {
		SessionToken string `json:"sessionToken"`
		RefreshToken string `json:"refreshToken"`
		Tenant       string `json:"tenant"`
	}
	if json.Unmarshal(raw, &out) == nil && out.SessionToken != "" {
		if path == "/v1/auth/refresh/fork" {
			b.forked = true
		}
		if out.RefreshToken != "" {
			b.refreshTok = out.RefreshToken
		}
		tok := out.SessionToken
		if target := strings.TrimSpace(b.cfg.Tenant); target != "" && target != out.Tenant {
			if switched := b.switchTenantToken(ctx, tok, target); switched != "" {
				tok = switched
			}
		}
		b.ownerTok = tok
		b.ownerTokAt = time.Now()
	}
	return b.ownerTok
}

// releaseSession revokes the bridge's own refresh family on a graceful stop
// (POST /v1/auth/logout, best-effort, bounded), so every desktop restart does
// not leave a dangling bridge sign-in in the member's session list. The
// handed-over token is never touched: it belongs to the member's client.
func (b *Bridge) releaseSession() {
	b.tokMu.Lock()
	tok := b.refreshTok
	b.refreshTok = ""
	b.tokMu.Unlock()
	if tok == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"refreshToken": tok})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.GatewayBaseURL+"/v1/auth/logout", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := b.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

// switchTenantToken re-scopes a freshly-minted owner token to the tenant the
// desktop UI is currently showing. Without this, a member who belongs to several
// tenants would run the bridge in their primary tenant while the UI is looking
// at another tenant's rooms, so agent chat is never consumed.
func (b *Bridge) switchTenantToken(ctx context.Context, token, tenantID string) string {
	body, _ := json.Marshal(map[string]string{"tenantId": tenantID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.GatewayBaseURL+"/v1/auth/switch-tenant", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var out struct {
		SessionToken string `json:"sessionToken"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out.SessionToken
}

// invalidateToken clears the cached owner token so the next gateway call mints a
// fresh one, picking up room memberships created since the last login.
func (b *Bridge) invalidateToken() {
	if b.cfg.GatewayToken != "" {
		return // static token: nothing to refresh
	}
	b.tokMu.Lock()
	b.ownerTok = ""
	b.tokMu.Unlock()
}

func (b *Bridge) do(ctx context.Context, method, url, token string, body any) ([]byte, error) {
	if body == nil {
		return b.doRaw(ctx, b.http, method, url, token, "", nil)
	}
	buf, _ := json.Marshal(body)
	return b.doRaw(ctx, b.http, method, url, token, "application/json", bytes.NewReader(buf))
}

// doRaw sends one request with a raw body (contentType empty = no body) and
// returns the response body, or a *gatewayError for a non-2xx answer.
func (b *Bridge) doRaw(ctx context.Context, client *http.Client, method, url, token, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: read response: %w", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if len(raw) > maxGatewayErrorBody {
			raw = raw[:maxGatewayErrorBody]
		}
		return nil, &gatewayError{Method: method, URL: url, Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// maxGatewayErrorBody bounds the response body a gatewayError keeps.
const maxGatewayErrorBody = 4 << 10

// gatewayError is a non-2xx gateway answer, kept structured so callers can
// tell a permanent rejection from a transient failure, and read a refusal's
// reason from its body (e.g. a borrow reservation's denyReason).
type gatewayError struct {
	Method string
	URL    string
	Status int
	Body   []byte
}

func (e *gatewayError) Error() string {
	return fmt.Sprintf("%s %s: status %d", e.Method, e.URL, e.Status)
}

// permanentGatewayError reports a rejection that retrying cannot fix: a 4xx
// other than timeout/rate-limit. Transport errors and 5xx are transient.
func permanentGatewayError(err error) bool {
	var ge *gatewayError
	if !errors.As(err, &ge) {
		return false
	}
	return ge.Status >= 400 && ge.Status < 500 &&
		ge.Status != http.StatusRequestTimeout && ge.Status != http.StatusTooManyRequests
}

// firstError returns the first non-nil error.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
