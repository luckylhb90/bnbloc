// Package manifestcore implements core.Provider by interpreting a declarative
// JSON manifest instead of hand-written Go, so a whole class of REST-daemon
// cores can be added by dropping a manifest file — no recompile, cross-platform
// (pure Go), and safe (data, not code).
//
// A manifest describes, per neutral verb, the HTTP method, a path template, an
// optional request-body template, and JSON-path extractions of the response,
// plus optional discovery and small rules (idle predicate, empty-target
// fallback, per-target catalog enrichment). Anything a manifest cannot express
// (multi-step handshakes, streaming control, exotic auth) stays a protocol
// adapter daemon.
//
// A loaded manifest registers under its Name through the same core registry as
// any other core, so the gateway/bridge treat it identically. Optional
// capabilities (interrupt, workspace-file writes, workspace deletion, session
// file reads, session workdir) are exposed through
// core.CapabilityReporter: the provider implements every optional method and
// reports a capability supported only when the manifest declares the matching
// op, so callers probing through core.As never see an unconfigured one.
package manifestcore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core"
)

// Provider interprets a Manifest as a core.Provider.
type Provider struct {
	m    Manifest
	HTTP *http.Client
	// ProbeToken is carried on discovery + as the endpoint token when the daemon
	// requires auth; injected by the composition root from config.
	ProbeToken string
	// ProbeURLOverride, when set, replaces the manifest's baked-in
	// discover.probeUrl so an operator can repoint the daemon (e.g. a builtin
	// manifest core) via BNBLOC_CORE_<NAME>_URL without editing the manifest.
	ProbeURLOverride string
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.m.Name }

// rest is the shared core REST client every manifest call goes through.
func (p *Provider) rest() core.REST {
	return core.REST{Name: "manifestcore " + p.m.Name, HTTP: p.HTTP}
}

// Discover implements core.Discoverer when the manifest declares a probe or a
// listener descriptor (or an endpoint override supplies a probe URL).
func (p *Provider) Discover(ctx context.Context) (core.Endpoint, bool) {
	if p.m.Discover == nil {
		return core.Endpoint{}, false
	}
	if p.m.Discover.Listener != nil {
		return p.discoverListener(p.m.Discover)
	}
	probeURL := strings.TrimSpace(p.ProbeURLOverride)
	if probeURL == "" {
		probeURL = strings.TrimSpace(p.m.Discover.ProbeURL)
	}
	if probeURL == "" {
		return core.Endpoint{}, false
	}
	ep := core.Endpoint{BaseURL: strings.TrimRight(probeURL, "/"), Token: p.ProbeToken}
	path := "/"
	if p.m.Discover.ProbePath != "" {
		path = p.m.Discover.ProbePath
	}
	// The probe is one more request through the shared REST client: the
	// same bearer, per-request bound and answer bound as every call.
	if _, err := p.rest().DoRaw(ctx, http.MethodGet, ep, path, nil, false); err != nil {
		return core.Endpoint{}, false
	}
	return ep, true
}

// discoverListener reads a daemon listener descriptor — the JSON file a daemon
// that binds a random port rewrites each run — and derives the endpoint from
// it. The descriptor's path comes from the env var the manifest names, else
// the manifest's default path; an explicit ProbeToken still wins over the
// file's token.
func (p *Provider) discoverListener(d *DiscoverOp) (core.Endpoint, bool) {
	l := d.Listener
	path := listenerPath(l)
	if path == "" {
		return core.Endpoint{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return core.Endpoint{}, false
	}
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return core.Endpoint{}, false
	}
	addr := strings.TrimSpace(jsonPathString(doc, l.AddrField))
	if addr == "" {
		return core.Endpoint{}, false
	}
	if rw := p.hostRewrite(d.HostRewrite); rw != "" {
		addr = strings.Replace(addr, "127.0.0.1", rw, 1)
		addr = strings.Replace(addr, "localhost", rw, 1)
	}
	token := ""
	if l.TokenField != "" {
		token = jsonPathString(doc, l.TokenField)
	}
	if p.ProbeToken != "" {
		token = p.ProbeToken
	}
	return core.Endpoint{BaseURL: "http://" + addr, Token: token}, true
}

// listenerPath resolves a listener descriptor path: the named env var when set
// and non-empty, else DefaultPath with a leading "~/" expanded to the home dir.
func listenerPath(l *ListenerSpec) string {
	if l.PathEnv != "" {
		if v := strings.TrimSpace(os.Getenv(l.PathEnv)); v != "" {
			return v
		}
	}
	path := strings.TrimSpace(l.DefaultPath)
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		return filepath.Join(home, rest)
	}
	return path
}

func (p *Provider) hostRewrite(spec *HostRewriteSpec) string {
	if spec == nil {
		return ""
	}
	if v, ok := os.LookupEnv(spec.Env); ok {
		return strings.TrimSpace(v)
	}
	return spec.Default
}

// ResolveTarget implements core.TargetResolver when the manifest declares a
// target id prefix (e.g. "local:"), deriving a target id from a provider
// name without a network call. An empty prefix reports not-resolvable so the
// caller falls back to live target listing.
func (p *Provider) ResolveTarget(provider string) (string, bool) {
	if p.m.TargetIDPrefix == "" {
		return "", false
	}
	return p.m.TargetIDPrefix + provider, true
}

// vars carries the template substitution values available to a call.
type vars map[string]string

// CreateWorkspace implements core.Provider.
func (p *Provider) CreateWorkspace(ctx context.Context, ep core.Endpoint) (string, error) {
	v := vars{"shortid": shortID()}
	out, err := p.call(ctx, ep, p.m.CreateWksp, v)
	if err != nil {
		return "", err
	}
	return p.m.CreateWksp.extractString(out, "workspaceId"), nil
}

// DeleteWorkspace implements core.WorkspaceDeleter (when the manifest declares
// deleteWorkspace; see Supports).
func (p *Provider) DeleteWorkspace(ctx context.Context, ep core.Endpoint, workspaceID string) error {
	v := vars{"workspaceId": workspaceID}
	_, err := p.call(ctx, ep, p.m.DeleteWksp, v)
	return err
}

// CreateSession implements core.Provider, resolving an empty target to the first
// listed target when the manifest requested the "firstTarget" fallback.
func (p *Provider) CreateSession(ctx context.Context, ep core.Endpoint, req core.SessionRequest) (core.SessionResult, error) {
	sessionID := core.UUIDv4()
	targetID := req.TargetID
	if targetID == "" && bodyWantsFirstTarget(p.m.CreateSession.Body) {
		ts, err := p.ListTargets(ctx, ep)
		if err != nil {
			return core.SessionResult{}, err
		}
		if len(ts) == 0 {
			return core.SessionResult{}, fmt.Errorf("manifestcore %q: no targets to bind", p.m.Name)
		}
		targetID = ts[0].ID
	}
	v := vars{
		"workspaceId":     req.WorkspaceID,
		"sessionId":       sessionID,
		"targetId":        targetID,
		"prompt":          req.Prompt,
		"shortid":         shortID(),
		"submitId":        core.UUIDv4(),
		"model":           req.Options.Model,
		"reasoningEffort": req.Options.ReasoningEffort,
	}
	for k, val := range profileVars(req.Profile) {
		v[k] = val
	}
	out, err := p.call(ctx, ep, p.m.CreateSession, v)
	if err != nil {
		return core.SessionResult{}, err
	}
	res := core.SessionResult{
		SessionID: p.m.CreateSession.extractString(out, "sessionId"),
		TurnID:    p.m.CreateSession.extractString(out, "turnId"),
		Phase:     p.m.CreateSession.extractString(out, "phase"),
	}
	if res.SessionID == "" {
		res.SessionID = sessionID
	}
	if res.SessionID == "" {
		return core.SessionResult{}, fmt.Errorf("manifestcore %q: no sessionId in response", p.m.Name)
	}
	return res, nil
}

// SendInput implements core.Provider. submitId is fresh per call: a manifest
// whose daemon requires a per-request idempotency key (e.g. a
// clientSubmitId) gets one that never repeats across retries.
func (p *Provider) SendInput(ctx context.Context, ep core.Endpoint, workspaceID, sessionID, text string, opts core.SessionOptions) error {
	v := vars{
		"workspaceId":     workspaceID,
		"sessionId":       sessionID,
		"text":            text,
		"submitId":        core.UUIDv4(),
		"model":           opts.Model,
		"reasoningEffort": opts.ReasoningEffort,
	}
	_, err := p.call(ctx, ep, p.m.SendInput, v)
	return err
}

// WriteWorkspaceFile implements core.WorkspaceFileWriter (when the manifest
// declares writeWorkspaceFile; see Supports).
func (p *Provider) WriteWorkspaceFile(ctx context.Context, ep core.Endpoint, workspaceID, relPath, content string) error {
	v := vars{"workspaceId": workspaceID, "relPath": relPath, "content": content, "contentBase64": ""}
	_, err := p.call(ctx, ep, p.m.WriteFile, v)
	return err
}

// WriteWorkspaceFileBytes implements core.WorkspaceFileBytesWriter. It reuses the writeWorkspaceFile op with a base64-encoded
// body: the manifest template selects {contentBase64} for binary files. The
// daemon decodes it.
func (p *Provider) WriteWorkspaceFileBytes(ctx context.Context, ep core.Endpoint, workspaceID, relPath string, content []byte) error {
	v := vars{"workspaceId": workspaceID, "relPath": relPath, "content": "", "contentBase64": base64.StdEncoding.EncodeToString(content)}
	_, err := p.call(ctx, ep, p.m.WriteFile, v)
	return err
}

// ListSessionFiles implements core.SessionFiles (when the manifest declares
// listSessionFiles + readSessionFile; see Supports): the op's items spec maps
// each listed element's path/bytes/mtimeMs.
func (p *Provider) ListSessionFiles(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) ([]core.SessionFile, error) {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID}
	out, err := p.call(ctx, ep, p.m.ListSessionFiles, v)
	if err != nil {
		return nil, err
	}
	return p.m.ListSessionFiles.extractFiles(out), nil
}

// ReadSessionFile implements core.SessionFiles: {relPath} is the listed file
// path (query-escaped when the template carries it as a query parameter).
// extract.contentBase64, when declared, locates the file's exact bytes
// (base64) in the response; otherwise extract.content locates its text.
func (p *Provider) ReadSessionFile(ctx context.Context, ep core.Endpoint, workspaceID, sessionID, relPath string) ([]byte, error) {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID, "relPath": relPath}
	out, err := p.call(ctx, ep, p.m.ReadSessionFile, v)
	if err != nil {
		return nil, err
	}
	if p.m.ReadSessionFile.extractPath("contentBase64") != "" {
		b, err := base64.StdEncoding.DecodeString(p.m.ReadSessionFile.extractString(out, "contentBase64"))
		if err != nil {
			return nil, fmt.Errorf("manifestcore %q: readSessionFile contentBase64: %w", p.m.Name, err)
		}
		return b, nil
	}
	return []byte(p.m.ReadSessionFile.extractString(out, "content")), nil
}

// SessionWorkdir implements core.SessionWorkdirReporter (when the manifest
// declares sessionWorkdir; see Supports) via extract.workdir.
func (p *Provider) SessionWorkdir(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) (string, error) {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID}
	out, err := p.call(ctx, ep, p.m.SessionWorkdir, v)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(p.m.SessionWorkdir.extractString(out, "workdir")), nil
}

// Interrupt implements core.TurnInterrupter (when the manifest declares an
// interrupt op; see Supports).
func (p *Provider) Interrupt(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) error {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID}
	_, err := p.call(ctx, ep, p.m.Interrupt, v)
	return err
}

// ListTargets implements core.Provider, then enriches each target's
// model/reasoning catalog when the manifest declares an enrich op.
func (p *Provider) ListTargets(ctx context.Context, ep core.Endpoint) ([]core.Target, error) {
	out, err := p.call(ctx, ep, p.m.ListTargets, vars{})
	if err != nil {
		return nil, err
	}
	targets := p.m.ListTargets.extractTargets(out)
	if spec := p.m.ListTargets.Targets; spec != nil && spec.Enrich != nil && len(targets) > 0 {
		p.enrichTargets(ctx, ep, targets, spec.Enrich)
	}
	return targets, nil
}

// enrichTargets fans the enrich call out across targets: each call is a
// separate daemon round-trip that may warm a provider, so serial calls make
// the roster O(targets) slow. Bounded per call so total latency stays ~one
// round-trip even if a provider is slow to warm.
func (p *Provider) enrichTargets(ctx context.Context, ep core.Endpoint, targets []core.Target, spec *EnrichSpec) {
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(t *core.Target) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			p.enrichTarget(cctx, ep, t, spec)
		}(&targets[i])
	}
	wg.Wait()
}

// enrichTarget applies the enrich call's catalog to one target. Best-effort:
// any failure leaves the target as-is (no picker), so a daemon that does not
// implement the call degrades cleanly.
func (p *Provider) enrichTarget(ctx context.Context, ep core.Endpoint, t *core.Target, spec *EnrichSpec) {
	v := vars{"provider": t.Provider, "targetId": t.ID}
	out, err := p.call(ctx, ep, Op{Method: spec.Method, Path: spec.Path, Body: spec.Body}, v)
	if err != nil || out == nil {
		return
	}
	for name, path := range spec.Fields {
		switch name {
		case "defaultModel":
			t.DefaultModel = firstNonEmptyPath(out, path)
		case "defaultReasoning":
			t.DefaultReasoning = firstNonEmptyPath(out, path)
		}
	}
	for name, proj := range spec.Lists {
		switch name {
		case "models":
			t.Models = listProjection(out, proj)
		case "reasoningEfforts":
			t.ReasoningEfforts = listProjection(out, proj)
		}
	}
	for name, proj := range spec.Maps {
		if name == "reasoningByModel" {
			t.ReasoningByModel = mapProjection(out, proj)
		}
	}
}

// Messages implements core.Provider through the messages op's items.
func (p *Provider) Messages(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string, after int64) ([]core.Message, int64, error) {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID, "after": fmt.Sprintf("%d", after)}
	out, err := p.call(ctx, ep, p.m.Messages, v)
	if err != nil {
		return nil, 0, err
	}
	latest := int64FromPath(out, p.m.Messages.extractPath("latestVersion"))
	return p.m.Messages.extractMessages(out), latest, nil
}

// PollTurn implements core.Provider: the pollTurn op's idleWhen predicate,
// plus a structured failure of a settled turn.
func (p *Provider) PollTurn(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) (core.TurnStatus, error) {
	v := vars{"workspaceId": workspaceID, "sessionId": sessionID}
	out, err := p.call(ctx, ep, p.m.PollTurn, v)
	if err != nil {
		return core.TurnStatus{}, err
	}
	idle := idleFromPredicate(out, p.m.PollTurn.IdleWhen)
	status := core.TurnStatus{Idle: idle}
	if idle && strings.EqualFold(p.m.PollTurn.extractString(out, "failed"), "failed") {
		status.Failed = true
		status.FailMessage = strings.TrimSpace(p.m.PollTurn.extractString(out, "failMessage"))
		if status.FailMessage == "" {
			status.FailMessage = strings.TrimSpace(p.m.PollTurn.extractString(out, "failCode"))
		}
	}
	return status, nil
}

// Supports implements core.CapabilityReporter: a manifest core offers an
// optional capability exactly when its manifest declares the matching op, so a
// caller probing through core.As never sees a capability that is not
// configured.
func (p *Provider) Supports(_ context.Context, _ core.Endpoint, c core.Capability) bool {
	switch c {
	case core.CapabilityInterrupt:
		return p.m.Interrupt.declared()
	case core.CapabilityWriteFile:
		return p.m.WriteFile.declared()
	case core.CapabilityDeleteWorkspace:
		return p.m.DeleteWksp.declared()
	case core.CapabilitySessionFiles:
		return p.m.ListSessionFiles.declared() && p.m.ReadSessionFile.declared()
	case core.CapabilitySessionFileBytes:
		return p.m.ListSessionFiles.declared() && p.m.ReadSessionFile.declared() &&
			p.m.ReadSessionFile.extractPath("contentBase64") != ""
	case core.CapabilitySessionWorkdir:
		return p.m.SessionWorkdir.declared()
	case core.CapabilityAgentProfile:
		return createSessionTakesProfile(p.m.CreateSession.Body)
	default:
		return false
	}
}

// call renders the op's path + body with vars and executes it through the
// shared core REST client (bearer, per-request timeout, status errors),
// returning the decoded JSON response (or nil for an empty body).
func (p *Provider) call(ctx context.Context, ep core.Endpoint, op Op, v vars) (any, error) {
	if !op.declared() {
		return nil, fmt.Errorf("manifestcore %q: op not declared in manifest", p.m.Name)
	}
	path := renderPath(op.Path+pathWhenTemplate(op.PathWhen, v), v)
	var body io.Reader
	if len(op.Body) > 0 {
		body = bytes.NewReader(renderJSON(op.Body, v))
	}
	raw, err := p.rest().DoRaw(ctx, op.Method, ep, path, body, body != nil)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("manifestcore %q: bad response json: %w", p.m.Name, err)
	}
	return out, nil
}

func shortID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
