package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// agentEndpoint identifies one member's local agent core daemon. Each
// collaborator runs their own core; the gateway proxies each caller's agent
// requests to THAT caller's daemon, so members use their own agents and
// credentials.
type agentEndpoint = core.Endpoint

// callerEndpoint resolves the core daemon endpoint for this request: the
// chosen core's configured/discovered (trusted) endpoint, or — only on a
// single-host dev gateway (see localAgentOverride) — the caller-supplied
// loopback override.
func (s *Service) callerEndpoint(entry *coreset.Entry, override agentEndpoint) agentEndpoint {
	if override.OK() {
		return override
	}
	if entry != nil {
		return entry.Endpoint
	}
	return agentEndpoint{}
}

// errLocalAgentOverrideDisabled refuses a caller-supplied local-agent address
// on a gateway that is not the single-host dev setup.
var errLocalAgentOverrideDisabled = fmt.Errorf("a local agent address override is a single-host dev feature and is disabled on this gateway")

// localAgentOverride validates a CALLER-SUPPLIED local-agent address. The
// gateway dials it from its OWN host, so "the caller's machine" is only the
// caller's machine when browser and gateway share one host (single-host dev).
// Everywhere else such a URL is an SSRF primitive into the gateway host and its
// network, so it is refused unless the dev discovery switch
// (BNBLOC_DEV_DISCOVERY) is on, and even then only a loopback IP literal is
// accepted — no hostnames (localhost, host.docker.internal, docker service
// names), no private ranges, no metadata endpoint. An empty raw URL means no
// override.
func localAgentOverride(rawURL, token string) (agentEndpoint, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return agentEndpoint{}, nil
	}
	if !devDiscoveryEnabled() {
		return agentEndpoint{}, errLocalAgentOverrideDisabled
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return agentEndpoint{}, fmt.Errorf("invalid local agent url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return agentEndpoint{}, fmt.Errorf("local agent url must be http(s)")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return agentEndpoint{}, fmt.Errorf("local agent url host %q is not permitted (loopback IP only)", host)
	}
	return agentEndpoint{BaseURL: rawURL, Token: strings.TrimSpace(token)}, nil
}

type localAgentRunResult struct {
	RoomID         string `json:"roomId"`
	AgentSessionID string `json:"agentSessionId"`
	TurnID         string `json:"turnId"`
	Phase          string `json:"phase"`
}

// handleLocalAgentRun creates a real agent session on one of the member-visible
// cores the gateway itself can reach (an installed endpoint/manifest core) and
// adopts its workspace as a bnbloc room. The address dialed is always the
// core's trusted, admin-installed endpoint; a caller-supplied address is only
// honored on a single-host dev gateway (localAgentOverride).
func (s *Service) handleLocalAgentRun(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// It creates a room and drives an agent: the proxy's org RBAC and rate
	// limit apply (edgeGate), not just a session.
	p, err := parseAPIPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.edgeGate(w, r, p, claims) {
		return
	}
	var body struct {
		Prompt          string `json:"prompt"`
		AgentTargetID   string `json:"agentTargetId"`
		Core            string `json:"core"`
		LocalAgentURL   string `json:"localAgentUrl"`
		LocalAgentToken string `json:"localAgentToken"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	override, err := localAgentOverride(body.LocalAgentURL, body.LocalAgentToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	// Pick the core: an explicit body.core wins, then the core encoded in the
	// qualified target id, else the default (first) core. The RAW target id
	// (core prefix stripped) is what the core's provider actually consumes.
	splitCore, rawTarget := core.SplitTargetID(strings.TrimSpace(body.AgentTargetID))
	coreName := strings.TrimSpace(body.Core)
	if coreName == "" {
		coreName = splitCore
	}
	visibleEntries, err := s.visibleCoreEntries(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	var entry *coreset.Entry
	if coreName != "" {
		for i := range visibleEntries {
			if visibleEntries[i].Name == coreName {
				entry = &visibleEntries[i]
				break
			}
		}
		if entry == nil {
			http.Error(w, "core not available", http.StatusNotFound)
			return
		}
	} else if len(visibleEntries) > 0 {
		entry = &visibleEntries[0]
	}
	if entry == nil {
		obs.ServerError(w, "no core configured", nil)
		http.Error(w, "no core configured", http.StatusServiceUnavailable)
		return
	}
	// The agent target is the caller's choice from /v1/local-agent/targets;
	// target ids are core data, so there is no gateway-side default to guess.
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		http.Error(w, "agentTargetId is required", http.StatusBadRequest)
		return
	}
	ep := s.callerEndpoint(entry, override)
	if !ep.OK() {
		obs.ServerError(w, "the selected core has no reachable endpoint", nil)
		http.Error(w, "the selected core has no reachable endpoint", http.StatusServiceUnavailable)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		body.Prompt = "Hello from a real local agent session"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	// Upstream failures are logged with detail but answered generically: a
	// core's or the room backend's error body is not the caller's to read.
	wsID, err := entry.Provider.CreateWorkspace(ctx, ep)
	if err != nil {
		localAgentRunFailed(w, "create_workspace", entry.Name, err)
		return
	}
	if err := s.adoptRoom(ctx, wsID, claims.Subject, claims.Tenant); err != nil {
		localAgentRunFailed(w, "adopt_room", entry.Name, err)
		return
	}
	res, err := entry.Provider.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: wsID, TargetID: target, Prompt: body.Prompt})
	if err != nil {
		localAgentRunFailed(w, "create_session", entry.Name, err)
		return
	}
	writeJSON(w, http.StatusOK, localAgentRunResult{
		RoomID: wsID, AgentSessionID: res.SessionID, TurnID: res.TurnID, Phase: res.Phase,
	})
}

func localAgentRunFailed(w http.ResponseWriter, step, coreName string, err error) {
	slog.Warn("bnbloc gateway local agent run failed",
		"event", "bnbloc.gateway.local_agent_run_failed", "step", step, "core", coreName, "error", err)
	http.Error(w, "local agent core request failed ("+step+")", http.StatusBadGateway)
}

// handleLocalAgentTargets lists the enabled agent targets across every
// configured core so the picker reflects all agents on the caller's machine,
// grouped by core. Each returned target id is core-qualified ("<core>/<rawid>")
// and carries its core name. A core whose endpoint is empty or unreachable is
// skipped (best-effort aggregation), so one dead core does not hide the others.
func (s *Service) handleLocalAgentTargets(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	visibleEntries, err := s.visibleCoreEntries(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	override, err := localAgentOverride(r.URL.Query().Get("localAgentUrl"), r.URL.Query().Get("localAgentToken"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// Which cores to query. A caller-supplied localAgentUrl override addresses a
	// single core (chosen by ?core=, else the default); without an override we
	// aggregate every configured core using its own endpoint.
	type query struct {
		entry *coreset.Entry
		ep    agentEndpoint
	}
	var queries []query
	if override.OK() {
		var entry *coreset.Entry
		if len(visibleEntries) > 0 {
			entry = &visibleEntries[0]
		}
		if name := strings.TrimSpace(r.URL.Query().Get("core")); name != "" {
			entry = nil
			for i := range visibleEntries {
				if visibleEntries[i].Name == name {
					entry = &visibleEntries[i]
					break
				}
			}
		}
		if entry != nil {
			queries = append(queries, query{entry: entry, ep: s.callerEndpoint(entry, override)})
		}
	} else {
		// Aggregate every visible core. Re-discover each first, so a daemon
		// started after gateway boot shows its agents on this request (and a
		// Re-scan) without a restart. Discovery runs on copies, never under
		// coresMu.
		refreshed := s.rediscoverCores(ctx, visibleEntries)
		for i := range refreshed {
			queries = append(queries, query{entry: &refreshed[i], ep: refreshed[i].Endpoint})
		}
	}

	out := make([]map[string]any, 0, len(queries))
	for _, q := range queries {
		if !q.ep.OK() {
			continue
		}
		targets, err := q.entry.Provider.ListTargets(ctx, q.ep)
		if err != nil {
			continue
		}
		for _, t := range targets {
			row := map[string]any{
				"id":       core.QualifyTargetID(q.entry.Name, t.ID),
				"provider": t.Provider,
				"core":     q.entry.Name,
			}
			// Model/reasoning pickers: forwarded only when the core discovered
			// them, so a core without a catalog simply offers no picker.
			if len(t.Models) > 0 {
				row["models"] = t.Models
			}
			if len(t.ReasoningEfforts) > 0 {
				row["reasoningEfforts"] = t.ReasoningEfforts
			}
			if len(t.ReasoningByModel) > 0 {
				row["reasoningByModel"] = t.ReasoningByModel
			}
			if t.DefaultModel != "" {
				row["defaultModel"] = t.DefaultModel
			}
			if t.DefaultReasoning != "" {
				row["defaultReasoning"] = t.DefaultReasoning
			}
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// adoptRoom makes the workspace id a bnbloc room the caller can use. It
// creates the room; when the id already exists (room ids are global, so a
// create in any tenant collides) it is adopted only if the caller is already
// a member — it is never re-created, and a room that belongs to someone else
// is refused rather than taken over.
func (s *Service) adoptRoom(ctx context.Context, roomID, subject, tenantID string) error {
	create, _ := json.Marshal(map[string]any{"roomId": roomID, "name": "Local agent room"})
	err := s.roomBackendCall(ctx, http.MethodPost, "/v1/rooms", subject, tenantID, create)
	var rbe *roomBackendError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rbe) && rbe.Status == http.StatusConflict:
		if !s.subjectInRoom(subject, roomID, tenantID) {
			return fmt.Errorf("room %q already exists and the caller is not a member", roomID)
		}
		return nil
	default:
		return err
	}
}

// roomBackendError is a non-2xx answer from the room service, kept structured
// so callers can act on the status (409 = id taken).
type roomBackendError struct {
	Path   string
	Status int
	Body   string
}

func (e *roomBackendError) Error() string {
	return fmt.Sprintf("room %s status %d: %s", e.Path, e.Status, e.Body)
}

// roomBackendCall calls the room service as subject in tenantID (the
// request's effective tenant), both signed like a proxied request.
func (s *Service) roomBackendCall(ctx context.Context, method, path, subject, tenantID string, body []byte) error {
	if s.Backends.Room == "" {
		return fmt.Errorf("room backend not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.Backends.Room, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bnbloc-User", subject)
	if tenantID != "" {
		req.Header.Set(svcauth.HeaderTenant, tenantID)
	}
	svcauth.Sign(req, svcauth.KeyFromEnv(), subject)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &roomBackendError{Path: path, Status: resp.StatusCode, Body: truncate(raw, 200)}
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleLocalAgentDiscover auto-detects a core's local daemon on the caller's
// host and returns its address + token so the web client can self-fill instead
// of asking the user to paste them. It reports the DEFAULT (first) core by
// default; an optional ?core=<name> selects a specific core. Discovery is the
// chosen core's own mechanism (one reads its listener descriptor; another core
// probes its default port), exposed via the core.Discoverer capability.
//
// Single-host only: a remote gateway cannot see a user's local daemon, so it
// returns {found:false} and the client falls back to manual entry.
func (s *Service) handleLocalAgentDiscover(w http.ResponseWriter, r *http.Request) {
	// This endpoint exposes the host's core access token, so it is dev-only:
	// unless BNBLOC_DEV_DISCOVERY=1 is set, it never authenticates, never
	// discovers, and never returns a token — it 404s as if the route did not
	// exist.
	if !devDiscoveryEnabled() {
		http.NotFound(w, r)
		return
	}
	if _, err := s.authenticate(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entry := s.defaultCore()
	if name := strings.TrimSpace(r.URL.Query().Get("core")); name != "" {
		entry = s.coreByName(name)
	}
	if entry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	d, ok := entry.Provider.(core.Discoverer)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ep, found := d.Discover(ctx)
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found":           true,
		"core":            entry.Name,
		"localAgentUrl":   ep.BaseURL,
		"localAgentToken": ep.Token,
	})
}

// devDiscoveryEnv is the dev-only single-host switch: /v1/local-agent/discover
// (which reads and returns the host core's token) and a caller-supplied core
// address on /v1/local-agent/run.
const devDiscoveryEnv = "BNBLOC_DEV_DISCOVERY"

// devDiscoveryEnabled reports whether the dev-only local-agent discovery path
// is explicitly enabled (BNBLOC_DEV_DISCOVERY=1). Never in production: the
// switch refuses startup there (CheckProductionEnv), and reads off here too.
func devDiscoveryEnabled() bool {
	if config.Production() {
		return false
	}
	v := strings.TrimSpace(os.Getenv(devDiscoveryEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// CheckProductionEnv refuses a production gateway (BNBLOC_ENV=production)
// started with a dev-only switch set. The composition root calls it before
// serving.
func CheckProductionEnv() error { return config.RefuseInProduction(devDiscoveryEnv) }
