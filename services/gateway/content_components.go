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

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
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
	// RecordJobRoomExpert records the room expert a job's turn wears (the
	// effective resolution decides it).
	store.RoomExpertRecorder
}

// Content request bounds.
const (
	maxContentFormBytes = 1 << 20
	// maxContentFileView bounds one file's text in a version detail, and
	// maxContentViewBytes all of them.
	maxContentFileView  = 256 << 10
	maxContentViewBytes = 2 << 20
)

// contentStoreErr answers a store failure (404 for a missing component).
func contentStoreErr(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, store.ErrContentNotFound) {
		httpx.Error(w, "content component not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, errContentReadOnly) {
		httpx.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, store.ErrContentOrgNameTaken) {
		httpx.Error(w, "the organization already has a component of that kind and name: remove the other one first", http.StatusConflict)
		return
	}
	obs.ServerError(w, what, err)
	httpx.Error(w, what, http.StatusInternalServerError)
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
	if !s.roomReachable(claims, roomID) {
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

// ownContent loads a personal component the caller authors (new versions,
// sharing, publishing). Someone else's is a 404, like a missing one; a
// shared copy or an org publication is not authored here (409).
func (s *Service) ownContent(ctx context.Context, claims auth.Claims, id string) (store.ContentComponent, error) {
	c, err := s.heldContent(ctx, claims, id)
	if err != nil {
		return store.ContentComponent{}, err
	}
	if c.Scope != store.ContentScopePersonal {
		return store.ContentComponent{}, errContentReadOnly
	}
	return c, nil
}

// ownedBy reports a component row the caller owns in the organization
// they act in: the one "is this mine" rule of the content routes (an owner
// id alone matches the same subject's rows in another organization).
func ownedBy(c store.ContentComponent, claims auth.Claims) bool {
	return c.Owner == claims.Subject && c.TenantID == claims.Tenant
}

// heldContent loads a component the caller holds in their own Library: a
// personal one or a shared copy (delete). An org publication belongs to
// the organization (its admins remove it).
func (s *Service) heldContent(ctx context.Context, claims auth.Claims, id string) (store.ContentComponent, error) {
	c, err := s.ContentComponents.GetContentComponent(ctx, id)
	if err != nil {
		return store.ContentComponent{}, err
	}
	if !ownedBy(c, claims) ||
		(c.Scope != store.ContentScopePersonal && c.Scope != store.ContentScopeShared) {
		return store.ContentComponent{}, store.ErrContentNotFound
	}
	return c, nil
}

// errContentReadOnly: a shared copy is read-only (update it to the author's
// latest instead).
var errContentReadOnly = errors.New("a shared copy is read-only: update it to the author's latest version, or ask its author for changes")

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
		Welcome:  m.Welcome, Uses: uses,
		Team:     contentuse.TeamRoster(m.Team),
		SharedBy: c.SharedBy, SourceID: c.SourceID,
		CreatedAtMs: c.CreatedAtMs, UpdatedAtMs: c.UpdatedAtMs,
	}
	if c.Kind == contentpkg.KindWork {
		// Answered 404 when the version has no cover: a listing never
		// opens a work's bundle (up to a site's size) to find out.
		out.CoverURL = "/v1/content-components/" + url.PathEscape(c.ID) + "/versions/" + url.PathEscape(v.Version) + "/cover"
		out.WorkType, out.WorkFile, out.Recipe = m.Type, m.File, m.Recipe
	}
	out.Assist = m.AssistSpec()
	held := ownedBy(c, claims)
	out.Mine = held && c.Scope == store.ContentScopePersonal
	out.Removable = held && (c.Scope == store.ContentScopePersonal || c.Scope == store.ContentScopeShared)
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	for _, t := range m.Requires.MCP {
		out.Requires.MCP = append(out.Requires.MCP, wire.ContentMCPRequirement{Name: t.Name, Settings: t.Settings})
	}
	for _, q := range m.QuickPrompts {
		out.QuickPrompts = append(out.QuickPrompts, wire.ContentQuickPrompt{Title: q.Title, Prompt: q.Prompt})
	}
	promptFields(m, &out)
	// Content the caller wrote needs no confirmation; a copy someone shared
	// with them does (the author is the trust owner).
	authored := c.Author() != "" && c.Author() == claims.Subject && c.TenantID == claims.Tenant
	out.Acked = authored || len(out.Permissions) == 0 || acked[c.ID+"@"+v.Version]
	return out, nil
}

// contentSkills fills the skill list of a view from the version's bundle
// (best-effort: a listing degrades to no skill list rather than failing).
// A snapshot kind (a work) has no skills, and its bundle is never opened
// for a listing.
func (s *Service) contentSkills(ctx context.Context, v store.ContentComponentVersion, view *wire.ContentComponent) {
	if spec, ok := contentpkg.Lookup(view.Kind); !ok || spec.Snapshot {
		return
	}
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
// one visibility function. kind is a full content kind ("" = every kind).
func (s *Service) memberContent(ctx context.Context, claims auth.Claims, full string) ([]wire.ContentComponent, error) {
	if s.ContentComponents == nil {
		return []wire.ContentComponent{}, nil
	}
	visible, err := contentuse.Visible(ctx, s.ContentComponents, viewerOf(claims), full)
	if err != nil {
		return nil, err
	}
	// The author sees their own organization publication on their personal
	// component (its publication), not as a second card; the card counts
	// the publication's uses and stars too.
	authored := map[string]bool{}
	for _, c := range visible {
		if c.Scope == store.ContentScopePersonal && ownedBy(c, claims) {
			authored[c.ID] = true
		}
	}
	comps := visible[:0:0]
	folded := map[string]string{} // personal id -> its publication's id
	for _, c := range visible {
		if c.Scope == store.ContentScopeOrg && ownedBy(c, claims) && authored[c.SourceID] {
			folded[c.SourceID] = c.ID
			continue
		}
		comps = append(comps, c)
	}
	views, err := s.contentViews(ctx, claims, comps, nil)
	if err != nil {
		return nil, err
	}
	if err := s.foldPublicationCounts(ctx, claims, views, folded); err != nil {
		return nil, err
	}
	sortContentViews(views)
	return views, nil
}

// foldPublicationCounts adds each folded publication's uses and stars to
// its author's card (folded: personal id -> publication id); the caller's
// star on either is their star.
func (s *Service) foldPublicationCounts(ctx context.Context, claims auth.Claims, views []wire.ContentComponent, folded map[string]string) error {
	if len(folded) == 0 {
		return nil
	}
	ids := make([]string, 0, len(folded))
	for _, id := range folded {
		ids = append(ids, id)
	}
	uses, err := s.ContentComponents.CountContentUses(ctx, claims.Tenant, ids)
	if err != nil {
		return err
	}
	stars, err := s.ContentComponents.CountContentFavorites(ctx, claims.Tenant, ids)
	if err != nil {
		return err
	}
	starred, err := s.ContentComponents.ListContentFavorites(ctx, claims.Tenant, claims.Subject, ids)
	if err != nil {
		return err
	}
	mine := map[string]bool{}
	for _, f := range starred {
		mine[f.ComponentID] = true
	}
	for i := range views {
		pub, ok := folded[views[i].ID]
		if !ok {
			continue
		}
		views[i].Uses += uses[pub]
		views[i].FavoriteCount += stars[pub]
		views[i].Favorite = views[i].Favorite || mine[pub]
	}
	return nil
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
	counts, err := s.ContentComponents.CountContentUses(ctx, claims.Tenant, idList)
	if err != nil {
		return nil, err
	}
	starred, err := s.ContentComponents.ListContentFavorites(ctx, claims.Tenant, claims.Subject, idList)
	if err != nil {
		return nil, err
	}
	favorite := map[string]bool{}
	for _, f := range starred {
		favorite[f.ComponentID] = true
	}
	stars, err := s.ContentComponents.CountContentFavorites(ctx, claims.Tenant, idList)
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
		view.Favorite, view.FavoriteCount = favorite[c.ID], stars[c.ID]
		s.contentSkills(ctx, v, &view)
		out = append(out, view)
	}
	if err := s.enrichPromptViews(ctx, claims, "", out); err != nil {
		return nil, err
	}
	if err := s.applyContentUsage(ctx, claims, out); err != nil {
		return nil, err
	}
	if err := s.applyContentSharing(ctx, claims, comps, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) handleListContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	var full string
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		k, ok := contentpkg.Lookup(kind)
		if !ok {
			httpx.Error(w, fmt.Sprintf("unknown content kind %q", kind), http.StatusBadRequest)
			return
		}
		full = k.Kind
	}
	list, err := s.memberContent(r.Context(), actor.Claims, full)
	if err != nil {
		obs.ServerError(w, "list content components failed", err)
		httpx.Error(w, "list content components failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"components": list})
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
			httpx.Error(w, "package too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		httpx.Error(w, "read body failed", http.StatusBadRequest)
		return nil, false
	}
	return raw, true
}

// handleCreateContent saves a new component: a JSON form, or a package
// archive whose component.json names it. With a job grant (?job=&room=,
// content_requester.go) the bridge running the job saves it as the job's
// requester.
func (s *Service) handleCreateContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
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
		raw, ok := readBody(w, r, contentpkg.BundleLimits().MaxArchiveBytes())
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
			httpx.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		version := firstNonEmpty(strings.TrimSpace(req.Version), "1.0.0")
		pkg, bundle, err = contentpkg.Build(req.Form, version, opts, nil)
	}
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
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
func (s *Service) handleImportContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	q := r.URL.Query()
	opts, ok := s.saveContentOptions(w, r, claims)
	if !ok {
		return
	}
	raw, ok := readBody(w, r, contentpkg.BundleLimits().MaxArchiveBytes())
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
			httpx.Error(w, err.Error(), http.StatusConflict)
			return
		}
		pkg, bundle, err := contentpkg.Import(raw, c.Kind, c.Name, version, opts)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.addContentVersion(w, r, contentActor{Claims: claims}, c, pkg, bundle)
		return
	}
	kind := strings.TrimSpace(q.Get("kind"))
	if _, ok := contentpkg.Lookup(kind); !ok {
		httpx.Error(w, fmt.Sprintf("unknown kind %q", kind), http.StatusBadRequest)
		return
	}
	version := firstNonEmpty(strings.TrimSpace(q.Get("version")), "1.0.0")
	pkg, bundle, err := contentpkg.Import(raw, kind, strings.TrimSpace(q.Get("name")), version, opts)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.createContent(w, r, contentActor{Claims: claims}, pkg, bundle)
}

// storeBundle puts a validated bundle into realtime's blob store.
func (s *Service) storeBundle(ctx context.Context, pkg *contentpkg.Package, bundle []byte) (string, error) {
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		return "", errors.New("artifact store not configured")
	}
	ref, err := s.blobs().PutBlob(ctx, bytes.NewReader(bundle))
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
		httpx.Error(w, "store content bundle failed", http.StatusBadGateway)
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
		httpx.Error(w, "encode content manifest failed", http.StatusInternalServerError)
		return
	}
	switch err := s.ContentComponents.CreateContentComponent(ctx, c, v); {
	case errors.Is(err, store.ErrContentExists):
		httpx.Error(w, fmt.Sprintf("you already have a %s named %q: save a new version of it instead", shortKind(m.Kind), m.Name), http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "save content component failed", err)
		httpx.Error(w, "save content component failed", http.StatusInternalServerError)
		return
	}
	c.LatestVersion = v.Version
	s.auditJobSave(ctx, actor, c, v.Version)
	s.contentSaved(ctx, actor, c, v.Version, wire.ContentSavedSaved, true)
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
func (s *Service) handleNewContentVersion(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
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
		raw, ok := readBody(w, r, contentpkg.BundleLimits().MaxArchiveBytes())
		if !ok {
			return
		}
		version, err := nextContentVersion(c, r.URL.Query().Get("version"))
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusConflict)
			return
		}
		pkg, bundle, err = contentpkg.Import(raw, c.Kind, c.Name, version, opts)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		raw, ok := readBody(w, r, maxContentFormBytes)
		if !ok {
			return
		}
		var req contentSaveRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			httpx.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		version, err := nextContentVersion(c, req.Version)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusConflict)
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
				httpx.Error(w, "load base version failed", http.StatusBadGateway)
				return
			}
		}
		req.Form.Kind, req.Form.Name = c.Kind, c.Name
		if pkg, bundle, err = contentpkg.Build(req.Form, version, opts, base); err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
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
		httpx.Error(w, fmt.Sprintf("a new version keeps the %s's kind and name (%s)", shortKind(c.Kind), c.Name), http.StatusBadRequest)
		return
	}
	if _, err := nextContentVersion(c, pkg.Manifest.Version); err != nil {
		httpx.Error(w, err.Error(), http.StatusConflict)
		return
	}
	key, err := s.storeBundle(ctx, pkg, bundle)
	if err != nil {
		obs.ServerError(w, "store content bundle failed", err)
		httpx.Error(w, "store content bundle failed", http.StatusBadGateway)
		return
	}
	now := s.nowTime().UnixMilli()
	v, err := contentVersionRow(pkg, key, len(bundle), claims.Subject, now)
	if err != nil {
		obs.ServerError(w, "encode content manifest failed", err)
		httpx.Error(w, "encode content manifest failed", http.StatusInternalServerError)
		return
	}
	next := c
	m := pkg.Manifest
	next.DisplayName, next.Description, next.Category, next.UpdatedAtMs = m.DisplayName, m.Description, m.Category, now
	switch err := s.ContentComponents.AddContentComponentVersion(ctx, next, c.LatestVersion, v); {
	case errors.Is(err, store.ErrContentVersionConflict):
		httpx.Error(w, "the component changed while saving: reload and save again", http.StatusConflict)
		return
	case err != nil:
		contentStoreErr(w, "save content version failed", err)
		return
	}
	next.LatestVersion = v.Version
	s.auditJobSave(ctx, actor, next, v.Version)
	s.contentSaved(ctx, actor, next, v.Version, wire.ContentSavedSaved, false)
	res := s.resubmitOrgPublication(ctx, r, actor, next, v.Version)
	s.writeContentDetailWith(w, r, claims, next, http.StatusCreated, func(d *wire.ContentComponentDetail) {
		d.Resubmitted, d.OrgPublished = res.request, res.published
	})
}

// writeContentDetail answers a component with its versions.
func (s *Service) writeContentDetail(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent, status int) {
	s.writeContentDetailWith(w, r, claims, c, status, nil)
}

// writeContentDetailWith answers a component with its versions, letting the
// caller add to the detail (a save's resubmitted request).
func (s *Service) writeContentDetailWith(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent, status int, extra func(*wire.ContentComponentDetail)) {
	ctx := r.Context()
	views, err := s.contentViews(ctx, claims, []store.ContentComponent{c}, nil)
	if err != nil || len(views) == 0 {
		if err == nil {
			err = errors.New("latest version missing")
		}
		obs.ServerError(w, "load content component failed", err)
		httpx.Error(w, "load content component failed", http.StatusInternalServerError)
		return
	}
	versions, err := s.ContentComponents.ListContentComponentVersions(ctx, c.ID)
	if err != nil {
		obs.ServerError(w, "load content versions failed", err)
		httpx.Error(w, "load content versions failed", http.StatusInternalServerError)
		return
	}
	out := wire.ContentComponentDetail{Component: views[0], Versions: make([]wire.ContentVersion, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, contentVersionView(v))
	}
	if extra != nil {
		extra(&out)
	}
	httpx.WriteJSON(w, status, out)
}

func contentVersionView(v store.ContentComponentVersion) wire.ContentVersion {
	return wire.ContentVersion{Version: v.Version, SHA256: v.SHA256, BundleKey: v.BundleBlobKey, BundleURL: pkgmeta.GatewayArtifactPath(v.BundleBlobKey),
		SizeBytes: v.SizeBytes, CreatedBy: v.CreatedBy, CreatedAtMs: v.CreatedAtMs}
}

func (s *Service) handleGetContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
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
	b, err := contentBundles.cache.LoadPinned(v.SHA256, contentpkg.BundleLimits(), func() ([]byte, error) {
		if raw, ok := contentbuiltin.Bundle(v.BundleBlobKey); ok {
			return raw, nil
		}
		return s.readArtifactBlob(ctx, v.BundleBlobKey, contentpkg.BundleLimits().MaxArchiveBytes())
	})
	if err != nil {
		return nil, err
	}
	return contentpkg.ValidateStored(b, v.ManifestJSON, contentOptions())
}

// handleGetContentVersion answers one version: the component as of that
// version, its instructions and skill bodies (the Library form) and its
// files (text when viewable).
func (s *Service) handleGetContentVersion(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
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
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	acks, err := s.ContentComponents.ListContentAcks(ctx, claims.Subject, []string{c.ID})
	if err != nil {
		obs.ServerError(w, "load content acks failed", err)
		httpx.Error(w, "load content acks failed", http.StatusInternalServerError)
		return
	}
	acked := map[string]bool{}
	for _, a := range acks {
		acked[a.ComponentID+"@"+a.Version] = true
	}
	view, err := contentView(c, v, claims, acked, 0)
	if err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	enriched := []wire.ContentComponent{view}
	if err := s.enrichPromptViews(ctx, claims, r.URL.Query().Get("room"), enriched); err != nil {
		obs.ServerError(w, "load prompt fields failed", err)
		httpx.Error(w, "load prompt fields failed", http.StatusInternalServerError)
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
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleDeleteContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	c, err := s.heldContent(r.Context(), claims, r.PathValue("id"))
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
func (s *Service) handleAckContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
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
	if err := s.ContentComponents.PutContentAck(ctx, store.ContentAck{Subject: claims.Subject, ComponentID: c.ID, Version: v.Version, AckedAtMs: s.nowTime().UnixMilli()}); err != nil {
		obs.ServerError(w, "record content ack failed", err)
		httpx.Error(w, "record content ack failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListContentUses(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	out := wire.ContentUsesReply{}
	var err error
	if out.Member, err = s.contentUseViews(ctx, claims, store.ContentUseMember, claims.Subject); err != nil {
		obs.ServerError(w, "list content uses failed", err)
		httpx.Error(w, "list content uses failed", http.StatusInternalServerError)
		return
	}
	if room := actor.Room; room != "" {
		// contentGrant checked the caller's reach of the room.
		if out.Room, err = s.contentUseViews(ctx, claims, store.ContentUseRoom, room); err != nil {
			obs.ServerError(w, "list content uses failed", err)
			httpx.Error(w, "list content uses failed", http.StatusInternalServerError)
			return
		}
		if out.RoomExpert, err = s.roomExpertOf(ctx, room); err != nil {
			obs.ServerError(w, "list room expert failed", err)
			httpx.Error(w, "list room expert failed", http.StatusInternalServerError)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
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
func (s *Service) handlePutContentUse(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	raw, ok := readBody(w, r, 64<<10)
	if !ok {
		return
	}
	var req wire.ContentUseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	claims, scope, scopeID, ok := s.contentUseScope(w, claims, req.Scope, req.RoomID)
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
	spec, _ := contentpkg.Lookup(c.Kind)
	if !spec.Use {
		httpx.Error(w, fmt.Sprintf("a %s is not turned on in a room or for a member", spec.Short), http.StatusBadRequest)
		return
	}
	if scope == store.ContentUseRoomExpert {
		s.putRoomExpert(w, r, claims, scopeID, c)
		return
	}
	if scope == store.ContentUseMember && !spec.MemberUse {
		httpx.Error(w, "only skill and prompt packs are turned on for you; pick an expert per message", http.StatusBadRequest)
		return
	}
	if version := strings.TrimSpace(req.Version); version != "" {
		if _, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, version); err != nil {
			contentStoreErr(w, "load content version failed", err)
			return
		}
	}
	if err := s.putContentUse(ctx, claims, scope, scopeID, c, strings.TrimSpace(req.Version)); err != nil {
		contentStoreErr(w, "save content use failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putContentUse turns c on (or pins it) in a use scope the caller was
// admitted to (contentUseScope: claims are the scope's) and announces the
// change: the one writer of a member's or a room's content uses (the
// Library's route and "Make similar" alike).
func (s *Service) putContentUse(ctx context.Context, claims auth.Claims, scope store.ContentUseScope, scopeID string, c store.ContentComponent, version string) error {
	u := store.ContentUse{TenantID: claims.Tenant, ScopeKind: scope, ScopeID: scopeID, ComponentID: c.ID, Kind: c.Kind,
		PinnedVersion: version, CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}
	if err := s.ContentComponents.PutContentUse(ctx, u); err != nil {
		return err
	}
	s.contentUsesChanged(ctx, claims, scope, scopeID, c.ID)
	return nil
}

// contentUseScope validates a use scope: "member" is the caller; "room"
// and "room-expert" need the room's owner or an admin, and act in the
// room's tenant (tenant_scope.go): the returned claims are the ones the
// use is written and announced with.
func (s *Service) contentUseScope(w http.ResponseWriter, claims auth.Claims, scope, roomID string) (auth.Claims, store.ContentUseScope, string, bool) {
	switch scope {
	case wire.ContentUseMember:
		return claims, store.ContentUseMember, claims.Subject, true
	case wire.ContentUseRoom, wire.ContentUseRoomExpert:
		roomID = strings.TrimSpace(roomID)
		if roomID == "" {
			httpx.Error(w, "roomId required", http.StatusBadRequest)
			return claims, "", "", false
		}
		editor, ok := s.roomEditorClaims(claims, roomID)
		if !ok {
			httpx.Error(w, "only the room's owner or admins change its experts and skills", http.StatusForbidden)
			return claims, "", "", false
		}
		return editor, store.ContentUseScope(scope), roomID, true
	default:
		httpx.Error(w, "scope must be member, room or room-expert", http.StatusBadRequest)
		return claims, "", "", false
	}
}

func (s *Service) handleDeleteContentUse(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	q := r.URL.Query()
	claims, scope, scopeID, ok := s.contentUseScope(w, claims, q.Get("scope"), q.Get("roomId"))
	if !ok {
		return
	}
	componentID := strings.TrimSpace(q.Get("componentId"))
	if err := s.ContentComponents.DeleteContentUse(r.Context(), scope, scopeID, componentID); err != nil {
		obs.ServerError(w, "delete content use failed", err)
		httpx.Error(w, "delete content use failed", http.StatusInternalServerError)
		return
	}
	if scope == store.ContentUseRoom {
		// The room expert is one of the room's pinned experts.
		if err := s.unpinRoomExpert(r.Context(), scopeID, componentID); err != nil {
			obs.ServerError(w, "clear room expert failed", err)
			httpx.Error(w, "clear room expert failed", http.StatusInternalServerError)
			return
		}
	}
	s.contentUsesChanged(r.Context(), claims, scope, scopeID, componentID)
	w.WriteHeader(http.StatusNoContent)
}

// handleContentEffective answers the bridge driving a job: the expert and
// skill packs its turn wears (contentuse.Effective). The bridge downloads
// each bundle with GET /v1/artifacts/{key}?job=&room=.
func (s *Service) handleContentEffective(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	if actor.Job == nil {
		httpx.Error(w, "job and room required", http.StatusBadRequest)
		return
	}
	job := *actor.Job
	eff, err := contentuse.Effective(r.Context(), s.ContentComponents, job)
	if err != nil {
		obs.ServerError(w, "resolve job content failed", err)
		httpx.Error(w, "resolve job content failed", http.StatusInternalServerError)
		return
	}
	if err := s.resolveEffectiveMCP(r.Context(), job, &eff); err != nil {
		obs.ServerError(w, "resolve job MCP tools failed", err)
		httpx.Error(w, "resolve job MCP tools failed", http.StatusInternalServerError)
		return
	}
	if err := s.recordRoomExpert(r.Context(), job, eff.RoomExpert); err != nil {
		obs.ServerError(w, "record job room expert failed", err)
		httpx.Error(w, "record job room expert failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, eff)
}

// jobAgent is a job's agent as audit details name it: owner/provider, or
// owner/core/provider when the job names its core.
func jobAgent(j store.AgentJob) string {
	if j.Core != "" {
		return j.OwnerUserID + "/" + j.Core + "/" + j.Provider
	}
	return j.OwnerUserID + "/" + j.Provider
}

// recordRoomExpert records the room expert the job's turn wears on the job
// (its run record names it beside the chat's expert) and, when that is new
// for the job, appends the content use audit row the chat's expert gets at
// post time. The room expert is resolved here, at turn time, so this is
// the one place it is known.
func (s *Service) recordRoomExpert(ctx context.Context, job store.AgentJob, ref *wire.ContentRef) error {
	if ref == nil || s.ContentJobs == nil {
		return nil
	}
	name := firstNonEmpty(ref.DisplayName, ref.Name)
	changed, err := s.ContentJobs.RecordJobRoomExpert(tenant.NewContext(ctx, job.TenantID), job.JobID, store.JobExpert{ID: ref.ComponentID, Version: ref.Version, Name: name})
	if err != nil || !changed {
		return err
	}
	s.audit(ctx, job.TenantID, store.ExtAuditRecord{
		Kind: wire.AuditKindContent, Name: ref.Name + "@" + ref.Version, Action: "use", Actor: job.RequesterUserID,
		Outcome: wire.AuditOutcomeRunning,
		Detail:  "room expert " + name + " v" + ref.Version + " on agent " + jobAgent(job) + " (job " + job.JobID + ", room " + job.RoomID + ")",
	})
	return nil
}

// contentBundleEntitled reports whether the actor (contentGrant, as the
// Library resolved it) may download a content bundle: a version of a
// component visible to them, of one the room they name uses (?room=), or
// one in effect for the job a granted request runs (?job=&room=; a staged
// draft the job wears has no version row and is granted this way only).
func (s *Service) contentBundleEntitled(ctx context.Context, actor contentActor, key string) (bool, error) {
	if s.ContentComponents == nil {
		return false, nil
	}
	comps, err := s.ContentComponents.ContentComponentsByBlob(ctx, store.ContentBlobQuery{
		Key: key, Subject: actor.Claims.Subject, Tenant: actor.Claims.Tenant, RoomID: actor.Room,
	})
	if err != nil {
		return false, err
	}
	for _, c := range comps {
		if extpipe.MemberVisibility(viewerOf(actor.Claims), extpipe.GateForContent(c), 0).Visible {
			return true, nil
		}
		if actor.Room != "" && s.contentInRoom(ctx, actor.Claims, actor.Room, c.ID) {
			return true, nil
		}
	}
	if actor.Job == nil {
		return false, nil
	}
	eff, err := contentuse.Effective(ctx, s.ContentComponents, *actor.Job)
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
