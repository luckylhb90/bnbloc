package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content components (docs/prd/2026-10-12-experts-and-teams.md §1): the
// member surface for experts and skill packs.
//
// Ownership: the gateway serves these routes itself. It already owns the
// component surface (GET /v1/components, opt-in installs, the artifact
// entitlement every bundle download goes through) and the platform install
// pipeline content components extend; it holds the store and realtime's
// blob client. Bundles are content-addressed blobs in realtime's store,
// exactly like sidecar and previewer artifacts, and every download goes
// through GET /v1/artifacts/{key} (artifactEntitled).

// ContentJobs is the job read side the content resolution of a running job
// needs: the job, and the owner of the bridge holding its lease.
type ContentJobs interface {
	GetAgentRun(ctx context.Context, jobID string) (store.AgentJob, error)
	store.AgentDispatchReader
	// ExpertUsage counts the jobs that wore experts (Library usage).
	store.ExpertUsageReader
}

// Content request bounds.
const (
	maxContentFormBytes = 1 << 20
	// maxContentFileView bounds one file's text in a version detail, and
	// maxContentViewBytes all of them.
	maxContentFileView  = 256 << 10
	maxContentViewBytes = 2 << 20
)

func (s *Service) registerContentComponents(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/content-components", s.handleListContent)
	mux.HandleFunc("POST /v1/content-components", s.handleCreateContent)
	mux.HandleFunc("POST /v1/content-components/import", s.handleImportContent)
	mux.HandleFunc("GET /v1/content-components/effective", s.handleContentEffective)
	mux.HandleFunc("GET /v1/content-components/{id}", s.handleGetContent)
	mux.HandleFunc("DELETE /v1/content-components/{id}", s.handleDeleteContent)
	mux.HandleFunc("POST /v1/content-components/{id}/versions", s.handleNewContentVersion)
	mux.HandleFunc("GET /v1/content-components/{id}/versions/{version}", s.handleGetContentVersion)
	mux.HandleFunc("POST /v1/content-components/{id}/versions/{version}/ack", s.handleAckContent)
	mux.HandleFunc("GET /v1/content-uses", s.handleListContentUses)
	mux.HandleFunc("PUT /v1/content-uses", s.handlePutContentUse)
	mux.HandleFunc("DELETE /v1/content-uses", s.handleDeleteContentUse)
}

// contentCaller authenticates a content request; write needs a role that
// may drive agents (viewers are read-only). It answers 401/403/503 itself.
func (s *Service) contentCaller(w http.ResponseWriter, r *http.Request, write bool) (auth.Claims, bool) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return auth.Claims{}, false
	}
	if s.ContentComponents == nil {
		obs.ServerError(w, "content components unavailable", nil)
		http.Error(w, "content components unavailable", http.StatusServiceUnavailable)
		return auth.Claims{}, false
	}
	if write {
		if !rbac.Can(claims.Role, rbac.PermDriveAgent) {
			http.Error(w, "insufficient org role", http.StatusForbidden)
			return auth.Claims{}, false
		}
		if !s.rateAllow(claims.Subject) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return auth.Claims{}, false
		}
	}
	return claims, true
}

// contentStoreErr answers a store failure (404 for a missing component).
func contentStoreErr(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, store.ErrContentNotFound) {
		http.Error(w, "content component not found", http.StatusNotFound)
		return
	}
	obs.ServerError(w, what, err)
	http.Error(w, what, http.StatusInternalServerError)
}

func shortKind(kind string) string {
	if k, ok := contentpkg.Lookup(kind); ok {
		return k.Short
	}
	return kind
}

// readableContent loads a component the caller may read: one visible to
// them, or one pinned/enabled in a room (?room=) they are a member of.
func (s *Service) readableContent(ctx context.Context, claims auth.Claims, id, roomID string) (store.ContentComponent, error) {
	c, err := s.ContentComponents.GetContentComponent(ctx, id)
	if err != nil {
		return store.ContentComponent{}, err
	}
	if extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
		return c, nil
	}
	if roomID != "" && s.contentInRoom(ctx, claims, roomID, id) {
		return c, nil
	}
	return store.ContentComponent{}, store.ErrContentNotFound
}

// contentInRoom reports whether a room the caller belongs to uses the
// component.
func (s *Service) contentInRoom(ctx context.Context, claims auth.Claims, roomID, id string) bool {
	if !claims.HasRoom(roomID) && !s.subjectInRoom(claims.Subject, roomID, claims.Tenant) {
		return false
	}
	uses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return false
	}
	for _, u := range uses {
		if u.ComponentID == id {
			return true
		}
	}
	return false
}

// ownContent loads a component the caller owns (writes: new versions,
// delete). Someone else's is a 404, like a missing one.
func (s *Service) ownContent(ctx context.Context, claims auth.Claims, id string) (store.ContentComponent, error) {
	c, err := s.ContentComponents.GetContentComponent(ctx, id)
	if err != nil {
		return store.ContentComponent{}, err
	}
	if c.Owner != claims.Subject || c.TenantID != claims.Tenant {
		return store.ContentComponent{}, store.ErrContentNotFound
	}
	return c, nil
}

// contentView turns a component at one version into its wire form.
func contentView(c store.ContentComponent, v store.ContentComponentVersion, claims auth.Claims, acked map[string]bool, uses int) (wire.ContentComponent, error) {
	m, err := contentuse.Manifest(v)
	if err != nil {
		return wire.ContentComponent{}, err
	}
	out := wire.ContentComponent{
		ID: c.ID, Kind: shortKind(c.Kind), Name: c.Name, DisplayName: m.DisplayName, Description: m.Description,
		Category: m.Category, Tags: m.Tags, Icon: m.Icon, Owner: c.Owner, Scope: string(c.Scope), Status: string(c.Status),
		LatestVersion: c.LatestVersion, Version: v.Version, Permissions: m.Permissions(), NeedsSandbox: m.NeedsSandbox(),
		Requires: wire.ContentRequires{Skills: m.Requires.Skills, Prompts: m.Requires.Prompts, Permissions: m.Requires.Permissions},
		Welcome:  m.Welcome, Mine: c.Owner == claims.Subject && c.TenantID == claims.Tenant, Uses: uses,
		Team:        contentuse.TeamRoster(m.Team),
		CreatedAtMs: c.CreatedAtMs, UpdatedAtMs: c.UpdatedAtMs,
	}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	for _, t := range m.Requires.MCP {
		out.Requires.MCP = append(out.Requires.MCP, wire.ContentMCPRequirement{Name: t.Name, Env: t.Env})
	}
	for _, q := range m.QuickPrompts {
		out.QuickPrompts = append(out.QuickPrompts, wire.ContentQuickPrompt{Title: q.Title, Prompt: q.Prompt})
	}
	promptFields(m, &out)
	out.Acked = out.Mine || len(out.Permissions) == 0 || acked[c.ID+"@"+v.Version]
	return out, nil
}

// contentSkills fills the skill list of a view from the version's bundle
// (best-effort: a listing degrades to no skill list rather than failing).
func (s *Service) contentSkills(ctx context.Context, v store.ContentComponentVersion, view *wire.ContentComponent) {
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		return
	}
	for _, sk := range pkg.Skills {
		view.Skills = append(view.Skills, wire.ContentSkill{Name: sk.Name, Description: sk.Description})
	}
}

// memberContent lists the content components visible to the caller at
// their latest version: the caller's own (personal and shared copies) and
// those published to their organization and the platform, all through the
// one visibility function.
func (s *Service) memberContent(ctx context.Context, claims auth.Claims, kind string) ([]wire.ContentComponent, error) {
	if s.ContentComponents == nil {
		return []wire.ContentComponent{}, nil
	}
	var full string
	if kind != "" {
		k, ok := contentpkg.Lookup(kind)
		if !ok {
			return nil, fmt.Errorf("unknown content kind %q", kind)
		}
		full = k.Kind
	}
	viewer := viewerOf(claims)
	seen := map[string]bool{}
	var comps []store.ContentComponent
	for _, q := range []store.ContentComponentQuery{
		{TenantID: claims.Tenant, Owner: claims.Subject, Kind: full, Scopes: []store.ContentScope{store.ContentScopePersonal, store.ContentScopeShared}},
		{TenantID: claims.Tenant, Kind: full, Scopes: []store.ContentScope{store.ContentScopeOrg}},
		{Kind: full, Scopes: []store.ContentScope{store.ContentScopePlatform}},
		{Kind: full, Scopes: []store.ContentScope{store.ContentScopeBuiltin}},
	} {
		list, err := s.ContentComponents.ListContentComponents(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, c := range list {
			if !seen[c.ID] && extpipe.MemberVisibility(viewer, extpipe.GateForContent(c), 0).Visible {
				seen[c.ID] = true
				comps = append(comps, c)
			}
		}
	}
	views, err := s.contentViews(ctx, claims, comps, nil)
	if err != nil {
		return nil, err
	}
	sortContentViews(views)
	return views, nil
}

// contentViews renders components at their latest (or pinned) version with
// the caller's acks and use counts.
func (s *Service) contentViews(ctx context.Context, claims auth.Claims, comps []store.ContentComponent, pinned map[string]string) ([]wire.ContentComponent, error) {
	out := make([]wire.ContentComponent, 0, len(comps))
	if len(comps) == 0 {
		return out, nil
	}
	idList := make([]string, 0, len(comps))
	for _, c := range comps {
		idList = append(idList, c.ID)
	}
	ackRows, err := s.ContentComponents.ListContentAcks(ctx, claims.Subject, idList)
	if err != nil {
		return nil, err
	}
	acked := map[string]bool{}
	for _, a := range ackRows {
		acked[a.ComponentID+"@"+a.Version] = true
	}
	counts, err := s.ContentComponents.CountContentUses(ctx, idList)
	if err != nil {
		return nil, err
	}
	for _, c := range comps {
		version := c.LatestVersion
		if p := pinned[c.ID]; p != "" {
			version = p
		}
		v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, version)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		view, err := contentView(c, v, claims, acked, counts[c.ID])
		if err != nil {
			return nil, err
		}
		s.contentSkills(ctx, v, &view)
		out = append(out, view)
	}
	if err := s.enrichPromptViews(ctx, claims, "", out); err != nil {
		return nil, err
	}
	if err := s.applyContentUsage(ctx, claims, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) handleListContent(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	actor, ok := s.contentActing(w, r, caller)
	if !ok {
		return
	}
	list, err := s.memberContent(r.Context(), actor.Claims, strings.TrimSpace(r.URL.Query().Get("kind")))
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown content kind") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		obs.ServerError(w, "list content components failed", err)
		http.Error(w, "list content components failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": list})
}

// contentSaveRequest is a JSON save: a form, the version to write
// (default: 1.0.0 for a new component, the next patch otherwise) and, for a
// new version, the version the form edits (its extra files carry over).
type contentSaveRequest struct {
	Form        contentpkg.Form `json:"form"`
	Version     string          `json:"version,omitempty"`
	BaseVersion string          `json:"baseVersion,omitempty"`
}

// isArchiveBody reports a request carrying a package archive (tar, tar.gz
// or zip) instead of a JSON form.
func isArchiveBody(r *http.Request) bool {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "application/json", "":
		return false
	}
	return true
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "package too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "read body failed", http.StatusBadRequest)
		return nil, false
	}
	return raw, true
}

// handleCreateContent saves a new component: a JSON form, or a package
// archive whose component.json names it. With a job grant (?job=&room=,
// content_requester.go) the bridge running the job saves it as the job's
// requester.
func (s *Service) handleCreateContent(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	actor, ok := s.contentActing(w, r, caller)
	if !ok {
		return
	}
	opts, ok := s.saveContentOptions(w, r, actor.Claims)
	if !ok {
		return
	}
	var pkg *contentpkg.Package
	var bundle []byte
	var err error
	if isArchiveBody(r) {
		raw, ok := readBody(w, r, tarbundle.ContentLimits.MaxArchiveBytes())
		if !ok {
			return
		}
		pkg, bundle, err = importArchive(raw, opts)
	} else {
		var req contentSaveRequest
		raw, ok := readBody(w, r, maxContentFormBytes)
		if !ok {
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		version := firstNonEmpty(strings.TrimSpace(req.Version), "1.0.0")
		pkg, bundle, err = contentpkg.Build(req.Form, version, opts, nil)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !jobSaveAllowed(w, actor, pkg) {
		return
	}
	s.createContent(w, r, actor, pkg, bundle)
}

// importArchive reads an uploaded bnbloc package archive: its own
// component.json names its kind.
func importArchive(raw []byte, opts contentpkg.Options) (*contentpkg.Package, []byte, error) {
	pkg, err := contentpkg.Validate(raw, "", opts)
	if err != nil {
		return nil, nil, err
	}
	return pkg, raw, nil
}

// contentOptions validate stored content when it is read: references are
// checked by syntax only (a save checked them against the catalog,
// saveContentOptions; an MCP tool uninstalled since shows as missing at use
// time instead of breaking the content).
func contentOptions() contentpkg.Options { return contentpkg.Options{} }

// handleImportContent imports an archive of an existing format as a new
// component (?kind=expert|skill&name=) or, with ?id=, as a new version of
// the caller's component.
func (s *Service) handleImportContent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts, ok := s.saveContentOptions(w, r, claims)
	if !ok {
		return
	}
	raw, ok := readBody(w, r, tarbundle.ContentLimits.MaxArchiveBytes())
	if !ok {
		return
	}
	if id := strings.TrimSpace(q.Get("id")); id != "" {
		c, err := s.ownContent(r.Context(), claims, id)
		if err != nil {
			contentStoreErr(w, "load content component failed", err)
			return
		}
		version, err := nextContentVersion(c, q.Get("version"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		pkg, bundle, err := contentpkg.Import(raw, c.Kind, c.Name, version, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.addContentVersion(w, r, contentActor{Claims: claims}, c, pkg, bundle)
		return
	}
	kind := strings.TrimSpace(q.Get("kind"))
	if _, ok := contentpkg.Lookup(kind); !ok {
		http.Error(w, fmt.Sprintf("unknown kind %q", kind), http.StatusBadRequest)
		return
	}
	version := firstNonEmpty(strings.TrimSpace(q.Get("version")), "1.0.0")
	pkg, bundle, err := contentpkg.Import(raw, kind, strings.TrimSpace(q.Get("name")), version, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.createContent(w, r, contentActor{Claims: claims}, pkg, bundle)
}

// storeBundle puts a validated bundle into realtime's blob store.
func (s *Service) storeBundle(ctx context.Context, pkg *contentpkg.Package, bundle []byte) (string, error) {
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		return "", errors.New("artifact store not configured")
	}
	ref, err := s.blobs().PutBlob(ctx, gatewayServiceSubject, bytes.NewReader(bundle))
	if err != nil {
		return "", err
	}
	if ref.SHA256 != "" && ref.SHA256 != pkg.SHA256 {
		return "", fmt.Errorf("blob store digest %s does not match the bundle %s", ref.SHA256, pkg.SHA256)
	}
	return ref.Key, nil
}

func (s *Service) createContent(w http.ResponseWriter, r *http.Request, actor contentActor, pkg *contentpkg.Package, bundle []byte) {
	ctx := r.Context()
	claims := actor.Claims
	key, err := s.storeBundle(ctx, pkg, bundle)
	if err != nil {
		obs.ServerError(w, "store content bundle failed", err)
		http.Error(w, "store content bundle failed", http.StatusBadGateway)
		return
	}
	now := s.nowTime().UnixMilli()
	m := pkg.Manifest
	c := store.ContentComponent{
		ID: ids.New("cc_"), Kind: m.Kind, Name: m.Name, TenantID: claims.Tenant, Owner: claims.Subject,
		Scope: store.ContentScopePersonal, Status: store.ContentStatusActive,
		DisplayName: m.DisplayName, Description: m.Description, Category: m.Category, CreatedAtMs: now, UpdatedAtMs: now,
	}
	v, err := contentVersionRow(pkg, key, len(bundle), claims.Subject, now)
	if err != nil {
		obs.ServerError(w, "encode content manifest failed", err)
		http.Error(w, "encode content manifest failed", http.StatusInternalServerError)
		return
	}
	switch err := s.ContentComponents.CreateContentComponent(ctx, c, v); {
	case errors.Is(err, store.ErrContentExists):
		http.Error(w, fmt.Sprintf("you already have a %s named %q: save a new version of it instead", shortKind(m.Kind), m.Name), http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "save content component failed", err)
		http.Error(w, "save content component failed", http.StatusInternalServerError)
		return
	}
	c.LatestVersion = v.Version
	s.auditJobSave(ctx, actor, c, v.Version)
	s.writeContentDetail(w, r, claims, c, http.StatusCreated)
}

func contentVersionRow(pkg *contentpkg.Package, key string, size int, actor string, now int64) (store.ContentComponentVersion, error) {
	manifest, err := json.Marshal(pkg.Manifest)
	if err != nil {
		return store.ContentComponentVersion{}, err
	}
	return store.ContentComponentVersion{
		Version: pkg.Manifest.Version, ManifestJSON: manifest, BundleBlobKey: key, SHA256: pkg.SHA256,
		SizeBytes: int64(size), CreatedBy: actor, CreatedAtMs: now,
	}, nil
}

// nextContentVersion is the version a new save writes: the requested one,
// which must be greater than the latest, else the latest's next patch.
func nextContentVersion(c store.ContentComponent, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return semver.BumpPatch(c.LatestVersion)
	}
	cmp, err := semver.Compare(requested, c.LatestVersion)
	if err != nil {
		return "", fmt.Errorf("version %q is not a semantic version", requested)
	}
	if cmp <= 0 {
		return "", fmt.Errorf("version %s is not newer than %s", requested, c.LatestVersion)
	}
	return requested, nil
}

// handleNewContentVersion saves the caller's component as a new immutable
// version: a JSON form (built on baseVersion's files) or a package archive.
func (s *Service) handleNewContentVersion(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	actor, ok := s.contentActing(w, r, caller)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	c, err := s.ownContent(ctx, claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	opts, ok := s.saveContentOptions(w, r, claims)
	if !ok {
		return
	}
	var pkg *contentpkg.Package
	var bundle []byte
	if isArchiveBody(r) {
		raw, ok := readBody(w, r, tarbundle.ContentLimits.MaxArchiveBytes())
		if !ok {
			return
		}
		version, err := nextContentVersion(c, r.URL.Query().Get("version"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		pkg, bundle, err = contentpkg.Import(raw, c.Kind, c.Name, version, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		raw, ok := readBody(w, r, maxContentFormBytes)
		if !ok {
			return
		}
		var req contentSaveRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		version, err := nextContentVersion(c, req.Version)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var base *contentpkg.Package
		if bv := strings.TrimSpace(req.BaseVersion); bv != "" {
			bver, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, bv)
			if err != nil {
				contentStoreErr(w, "load base version failed", err)
				return
			}
			if base, err = s.contentPackage(ctx, bver); err != nil {
				obs.ServerError(w, "load base version failed", err)
				http.Error(w, "load base version failed", http.StatusBadGateway)
				return
			}
		}
		req.Form.Kind, req.Form.Name = c.Kind, c.Name
		if pkg, bundle, err = contentpkg.Build(req.Form, version, opts, base); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if !jobSaveAllowed(w, actor, pkg) {
		return
	}
	s.addContentVersion(w, r, actor, c, pkg, bundle)
}

func (s *Service) addContentVersion(w http.ResponseWriter, r *http.Request, actor contentActor, c store.ContentComponent, pkg *contentpkg.Package, bundle []byte) {
	ctx := r.Context()
	claims := actor.Claims
	if pkg.Manifest.Kind != c.Kind || pkg.Manifest.Name != c.Name {
		http.Error(w, fmt.Sprintf("a new version keeps the %s's kind and name (%s)", shortKind(c.Kind), c.Name), http.StatusBadRequest)
		return
	}
	if _, err := nextContentVersion(c, pkg.Manifest.Version); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	key, err := s.storeBundle(ctx, pkg, bundle)
	if err != nil {
		obs.ServerError(w, "store content bundle failed", err)
		http.Error(w, "store content bundle failed", http.StatusBadGateway)
		return
	}
	now := s.nowTime().UnixMilli()
	v, err := contentVersionRow(pkg, key, len(bundle), claims.Subject, now)
	if err != nil {
		obs.ServerError(w, "encode content manifest failed", err)
		http.Error(w, "encode content manifest failed", http.StatusInternalServerError)
		return
	}
	next := c
	m := pkg.Manifest
	next.DisplayName, next.Description, next.Category, next.UpdatedAtMs = m.DisplayName, m.Description, m.Category, now
	switch err := s.ContentComponents.AddContentComponentVersion(ctx, next, c.LatestVersion, v); {
	case errors.Is(err, store.ErrContentVersionConflict):
		http.Error(w, "the component changed while saving: reload and save again", http.StatusConflict)
		return
	case err != nil:
		contentStoreErr(w, "save content version failed", err)
		return
	}
	next.LatestVersion = v.Version
	s.auditJobSave(ctx, actor, next, v.Version)
	s.writeContentDetail(w, r, claims, next, http.StatusCreated)
}

// writeContentDetail answers a component with its versions.
func (s *Service) writeContentDetail(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent, status int) {
	ctx := r.Context()
	views, err := s.contentViews(ctx, claims, []store.ContentComponent{c}, nil)
	if err != nil || len(views) == 0 {
		if err == nil {
			err = errors.New("latest version missing")
		}
		obs.ServerError(w, "load content component failed", err)
		http.Error(w, "load content component failed", http.StatusInternalServerError)
		return
	}
	versions, err := s.ContentComponents.ListContentComponentVersions(ctx, c.ID)
	if err != nil {
		obs.ServerError(w, "load content versions failed", err)
		http.Error(w, "load content versions failed", http.StatusInternalServerError)
		return
	}
	out := wire.ContentComponentDetail{Component: views[0], Versions: make([]wire.ContentVersion, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, contentVersionView(v))
	}
	writeJSON(w, status, out)
}

func contentVersionView(v store.ContentComponentVersion) wire.ContentVersion {
	return wire.ContentVersion{Version: v.Version, SHA256: v.SHA256, BundleKey: v.BundleBlobKey, BundleURL: "/v1/artifacts/" + v.BundleBlobKey,
		SizeBytes: v.SizeBytes, CreatedBy: v.CreatedBy, CreatedAtMs: v.CreatedAtMs}
}

func (s *Service) handleGetContent(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	actor, ok := s.contentActing(w, r, caller)
	if !ok {
		return
	}
	claims := actor.Claims
	c, err := s.readableContent(r.Context(), claims, r.PathValue("id"), r.URL.Query().Get("room"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	s.writeContentDetail(w, r, claims, c, http.StatusOK)
}

// contentBundles caches parsed content bundles by sha256 (gateway views).
var contentBundles = struct {
	once  sync.Once
	cache *tarbundle.Cache
}{}

// contentPackage loads and validates a version's bundle (cached by digest).
func (s *Service) contentPackage(ctx context.Context, v store.ContentComponentVersion) (*contentpkg.Package, error) {
	contentBundles.once.Do(func() { contentBundles.cache = tarbundle.NewCache(64 << 20) })
	b, err := contentBundles.cache.GetOrLoad(v.SHA256, func() (*tarbundle.Bundle, error) {
		raw, ok := contentbuiltin.Bundle(v.BundleBlobKey)
		if !ok {
			var err error
			if raw, err = s.readArtifactBlob(ctx, v.BundleBlobKey, tarbundle.ContentLimits.MaxArchiveBytes()); err != nil {
				return nil, err
			}
		}
		return tarbundle.Parse(raw, v.SHA256, tarbundle.ContentLimits)
	})
	if err != nil {
		return nil, err
	}
	return contentpkg.ValidateBundle(b, contentOptions())
}

// handleGetContentVersion answers one version: the component as of that
// version, its instructions and skill bodies (the Library form) and its
// files (text when viewable).
func (s *Service) handleGetContentVersion(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	actor, ok := s.contentActing(w, r, caller)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	c, err := s.readableContent(ctx, claims, r.PathValue("id"), r.URL.Query().Get("room"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, r.PathValue("version"))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		http.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	acks, err := s.ContentComponents.ListContentAcks(ctx, claims.Subject, []string{c.ID})
	if err != nil {
		obs.ServerError(w, "load content acks failed", err)
		http.Error(w, "load content acks failed", http.StatusInternalServerError)
		return
	}
	acked := map[string]bool{}
	for _, a := range acks {
		acked[a.ComponentID+"@"+a.Version] = true
	}
	view, err := contentView(c, v, claims, acked, 0)
	if err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		http.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	enriched := []wire.ContentComponent{view}
	if err := s.enrichPromptViews(ctx, claims, r.URL.Query().Get("room"), enriched); err != nil {
		obs.ServerError(w, "load prompt fields failed", err)
		http.Error(w, "load prompt fields failed", http.StatusInternalServerError)
		return
	}
	view = enriched[0]
	out := wire.ContentVersionDetail{Component: view, Version: contentVersionView(v), Instructions: pkg.Instructions, SkillBodies: map[string]string{}, Files: []wire.ContentFile{}}
	for _, sk := range pkg.Skills {
		out.Component.Skills = append(out.Component.Skills, wire.ContentSkill{Name: sk.Name, Description: sk.Description})
		if raw, ok := pkg.Bundle.File(sk.Dir + "/SKILL.md"); ok {
			_, body := contentpkg.ParseFrontmatter(string(raw))
			out.SkillBodies[sk.Name] = body
		}
	}
	budget := maxContentViewBytes
	for _, p := range pkg.Bundle.Paths() {
		data, _ := pkg.Bundle.File(p)
		f := wire.ContentFile{Path: p, SizeBytes: len(data)}
		switch {
		case !utf8.Valid(data):
			f.Binary = true
		case len(data) <= maxContentFileView && len(data) <= budget:
			f.Text = string(data)
			budget -= len(data)
		}
		out.Files = append(out.Files, f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleDeleteContent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	c, err := s.ownContent(r.Context(), claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if err := s.ContentComponents.DeleteContentComponent(r.Context(), c.ID); err != nil {
		contentStoreErr(w, "delete content component failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAckContent records that the caller confirmed a version's declared
// permissions (first use).
func (s *Service) handleAckContent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := s.readableContent(ctx, claims, r.PathValue("id"), r.URL.Query().Get("room"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, r.PathValue("version"))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	if err := s.ContentComponents.PutContentAck(ctx, store.ContentAck{Subject: claims.Subject, ComponentID: c.ID, Version: v.Version, AckedAtMs: s.nowTime().UnixMilli()}); err != nil {
		obs.ServerError(w, "record content ack failed", err)
		http.Error(w, "record content ack failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// roomEditor reports whether the caller may change a room's content
// settings (pinned experts, room skill packs): its owner or an admin.
func (s *Service) roomEditor(claims auth.Claims, roomID string) bool {
	if s.RoomRole == nil {
		return false
	}
	role, human := s.RoomRole(claims.Subject, roomID)
	return human && (role == string(store.RoleOwner) || role == string(store.RoleAdmin))
}

func (s *Service) handleListContentUses(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	out := wire.ContentUsesReply{}
	var err error
	if out.Member, err = s.contentUseViews(ctx, claims, store.ContentUseMember, claims.Subject); err != nil {
		obs.ServerError(w, "list content uses failed", err)
		http.Error(w, "list content uses failed", http.StatusInternalServerError)
		return
	}
	if room := strings.TrimSpace(r.URL.Query().Get("room")); room != "" {
		if !claims.HasRoom(room) && !s.subjectInRoom(claims.Subject, room, claims.Tenant) {
			http.Error(w, "room scope not authorized", http.StatusForbidden)
			return
		}
		if out.Room, err = s.contentUseViews(ctx, claims, store.ContentUseRoom, room); err != nil {
			obs.ServerError(w, "list content uses failed", err)
			http.Error(w, "list content uses failed", http.StatusInternalServerError)
			return
		}
		if out.RoomExpert, err = s.roomExpertOf(ctx, room); err != nil {
			obs.ServerError(w, "list room expert failed", err)
			http.Error(w, "list room expert failed", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// contentUseViews lists a scope's uses with each component at the version
// in effect (pinned, else latest).
func (s *Service) contentUseViews(ctx context.Context, claims auth.Claims, scope store.ContentUseScope, scopeID string) ([]wire.ContentUse, error) {
	uses, err := s.ContentComponents.ListContentUses(ctx, scope, scopeID)
	if err != nil {
		return nil, err
	}
	out := make([]wire.ContentUse, 0, len(uses))
	if len(uses) == 0 {
		return out, nil
	}
	idList := make([]string, 0, len(uses))
	pinned := map[string]string{}
	for _, u := range uses {
		idList = append(idList, u.ComponentID)
		pinned[u.ComponentID] = u.PinnedVersion
	}
	comps, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{IDs: idList})
	if err != nil {
		return nil, err
	}
	views, err := s.contentViews(ctx, claims, comps, pinned)
	if err != nil {
		return nil, err
	}
	if scope == store.ContentUseRoom {
		// A room's experts may point at prompt packs turned on in it.
		if err := s.enrichPromptViews(ctx, claims, scopeID, views); err != nil {
			return nil, err
		}
	}
	byID := map[string]wire.ContentComponent{}
	for _, v := range views {
		byID[v.ID] = v
	}
	for _, u := range uses {
		view, ok := byID[u.ComponentID]
		if !ok {
			continue
		}
		wu := wire.ContentUse{Scope: string(u.ScopeKind), ScopeID: u.ScopeID, ComponentID: u.ComponentID, Kind: shortKind(u.Kind),
			PinnedVersion: u.PinnedVersion, CreatedBy: u.CreatedBy, CreatedAtMs: u.CreatedAtMs, Component: &view}
		if u.PinnedVersion != "" {
			if cmp, err := semver.Compare(view.LatestVersion, u.PinnedVersion); err == nil && cmp > 0 {
				wu.UpdateAvailable = true
			}
		}
		out = append(out, wu)
	}
	return out, nil
}

// handlePutContentUse enables a skill pack for the caller (all their
// agents), or a skill pack / expert for a room (its owner or admins), at a
// pinned version or following the latest. The caller must be able to use
// the component themselves.
func (s *Service) handlePutContentUse(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	ctx := r.Context()
	raw, ok := readBody(w, r, 64<<10)
	if !ok {
		return
	}
	var req wire.ContentUseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	scope, scopeID, ok := s.contentUseScope(w, claims, req.Scope, req.RoomID)
	if !ok {
		return
	}
	c, err := s.ContentComponents.GetContentComponent(ctx, strings.TrimSpace(req.ComponentID))
	if err == nil && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Installed && !(scope != store.ContentUseMember && s.contentInRoom(ctx, claims, scopeID, c.ID)) {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if scope == store.ContentUseRoomExpert {
		s.putRoomExpert(w, r, claims, scopeID, c)
		return
	}
	if spec, _ := contentpkg.Lookup(c.Kind); scope == store.ContentUseMember && !spec.MemberUse {
		http.Error(w, "only skill and prompt packs are turned on for you; pick an expert per message", http.StatusBadRequest)
		return
	}
	if version := strings.TrimSpace(req.Version); version != "" {
		if _, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, version); err != nil {
			contentStoreErr(w, "load content version failed", err)
			return
		}
	}
	u := store.ContentUse{TenantID: claims.Tenant, ScopeKind: scope, ScopeID: scopeID, ComponentID: c.ID, Kind: c.Kind,
		PinnedVersion: strings.TrimSpace(req.Version), CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}
	if err := s.ContentComponents.PutContentUse(ctx, u); err != nil {
		contentStoreErr(w, "save content use failed", err)
		return
	}
	s.contentUsesChanged(ctx, claims, scope, scopeID, c.ID)
	w.WriteHeader(http.StatusNoContent)
}

// contentUseScope validates a use scope: "member" is the caller; "room"
// and "room-expert" need the room's owner or an admin.
func (s *Service) contentUseScope(w http.ResponseWriter, claims auth.Claims, scope, roomID string) (store.ContentUseScope, string, bool) {
	switch scope {
	case wire.ContentUseMember:
		return store.ContentUseMember, claims.Subject, true
	case wire.ContentUseRoom, wire.ContentUseRoomExpert:
		roomID = strings.TrimSpace(roomID)
		if roomID == "" {
			http.Error(w, "roomId required", http.StatusBadRequest)
			return "", "", false
		}
		if !s.roomEditor(claims, roomID) {
			http.Error(w, "only the room's owner or admins change its experts and skills", http.StatusForbidden)
			return "", "", false
		}
		return store.ContentUseScope(scope), roomID, true
	default:
		http.Error(w, "scope must be member, room or room-expert", http.StatusBadRequest)
		return "", "", false
	}
}

func (s *Service) handleDeleteContentUse(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope, scopeID, ok := s.contentUseScope(w, claims, q.Get("scope"), q.Get("roomId"))
	if !ok {
		return
	}
	componentID := strings.TrimSpace(q.Get("componentId"))
	if err := s.ContentComponents.DeleteContentUse(r.Context(), scope, scopeID, componentID); err != nil {
		obs.ServerError(w, "delete content use failed", err)
		http.Error(w, "delete content use failed", http.StatusInternalServerError)
		return
	}
	if scope == store.ContentUseRoom {
		// The room expert is one of the room's pinned experts.
		if err := s.unpinRoomExpert(r.Context(), scopeID, componentID); err != nil {
			obs.ServerError(w, "clear room expert failed", err)
			http.Error(w, "clear room expert failed", http.StatusInternalServerError)
			return
		}
	}
	s.contentUsesChanged(r.Context(), claims, scope, scopeID, componentID)
	w.WriteHeader(http.StatusNoContent)
}

// contentJob loads the job a bridge resolves content for: in the room's
// tenant, in that room, live and leased by the caller's bridge
// (store.JobLeasedBy). It answers the failure itself.
func (s *Service) contentJob(w http.ResponseWriter, r *http.Request, claims auth.Claims, jobID, roomID string) (store.AgentJob, bool) {
	if s.ContentJobs == nil {
		obs.ServerError(w, "agent jobs unavailable", nil)
		http.Error(w, "agent jobs unavailable", http.StatusServiceUnavailable)
		return store.AgentJob{}, false
	}
	if jobID == "" || roomID == "" {
		http.Error(w, "job and room required", http.StatusBadRequest)
		return store.AgentJob{}, false
	}
	if !claims.HasRoom(roomID) && !s.subjectInRoom(claims.Subject, roomID, claims.Tenant) {
		http.Error(w, "room scope not authorized", http.StatusForbidden)
		return store.AgentJob{}, false
	}
	tid := claims.Tenant
	if s.RoomTenant != nil {
		if rt := s.RoomTenant(roomID); rt != "" {
			tid = rt
		}
	}
	ctx := tenant.NewContext(r.Context(), tid)
	job, err := s.ContentJobs.GetAgentRun(ctx, jobID)
	if errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && job.RoomID != roomID) {
		http.Error(w, "job not found", http.StatusNotFound)
		return store.AgentJob{}, false
	}
	if err != nil {
		obs.ServerError(w, "load agent job failed", err)
		http.Error(w, "load agent job failed", http.StatusInternalServerError)
		return store.AgentJob{}, false
	}
	switch err := store.JobLeasedBy(ctx, s.ContentJobs, job, claims.Subject); {
	case errors.Is(err, store.ErrJobNotLive):
		http.Error(w, "job is not running", http.StatusConflict)
		return store.AgentJob{}, false
	case errors.Is(err, store.ErrJobNotCallers):
		http.Error(w, "job is not running on your bridge", http.StatusForbidden)
		return store.AgentJob{}, false
	case err != nil:
		obs.ServerError(w, "load bridge owner failed", err)
		http.Error(w, "load bridge owner failed", http.StatusInternalServerError)
		return store.AgentJob{}, false
	}
	if job.TenantID == "" {
		job.TenantID = tid
	}
	return job, true
}

// handleContentEffective answers the bridge driving a job: the expert and
// skill packs its turn wears (contentuse.Effective). The bridge downloads
// each bundle with GET /v1/artifacts/{key}?job=&room=.
func (s *Service) handleContentEffective(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	job, ok := s.contentJob(w, r, claims, strings.TrimSpace(q.Get("job")), strings.TrimSpace(q.Get("room")))
	if !ok {
		return
	}
	eff, err := contentuse.Effective(r.Context(), s.ContentComponents, job)
	if err != nil {
		obs.ServerError(w, "resolve job content failed", err)
		http.Error(w, "resolve job content failed", http.StatusInternalServerError)
		return
	}
	if err := s.resolveEffectiveMCP(r.Context(), job, &eff); err != nil {
		obs.ServerError(w, "resolve job MCP tools failed", err)
		http.Error(w, "resolve job MCP tools failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, eff)
}

// contentBundleEntitled reports whether the caller may download a content
// bundle: a version of a component visible to them, of one their room
// uses (?room=), or one in effect for a job their bridge runs (?job=).
func (s *Service) contentBundleEntitled(ctx context.Context, claims auth.Claims, key string, grant url.Values) (bool, error) {
	if s.ContentComponents == nil {
		return false, nil
	}
	versions, err := s.ContentComponents.ContentVersionsByBlob(ctx, key)
	if err != nil || len(versions) == 0 {
		return false, err
	}
	roomID := strings.TrimSpace(grant.Get("room"))
	for _, v := range versions {
		c, err := s.ContentComponents.GetContentComponent(ctx, v.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
			return true, nil
		}
		if roomID != "" && s.contentInRoom(ctx, claims, roomID, c.ID) {
			return true, nil
		}
	}
	jobID := strings.TrimSpace(grant.Get("job"))
	if jobID == "" || roomID == "" || s.ContentJobs == nil {
		return false, nil
	}
	tid := claims.Tenant
	if s.RoomTenant != nil {
		if rt := s.RoomTenant(roomID); rt != "" {
			tid = rt
		}
	}
	jctx := tenant.NewContext(ctx, tid)
	job, err := s.ContentJobs.GetAgentRun(jctx, jobID)
	if errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && job.RoomID != roomID) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := store.JobLeasedBy(jctx, s.ContentJobs, job, claims.Subject); errors.Is(err, store.ErrJobNotLive) || errors.Is(err, store.ErrJobNotCallers) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if job.TenantID == "" {
		job.TenantID = tid
	}
	eff, err := contentuse.Effective(ctx, s.ContentComponents, job)
	if err != nil {
		return false, err
	}
	return contentuse.BundleKeys(eff)[key], nil
}

// sortContentViews orders a listing by kind, then most recently updated.
func sortContentViews(list []wire.ContentComponent) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		return list[i].UpdatedAtMs > list[j].UpdatedAtMs
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
