package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Organization approval of content components (docs/prd/2026-10-12-
// experts-and-teams.md §1, step 4): org admins (manage_org, their own
// organization; a super admin may act in any, as on the other tenant
// routes) review publish requests and manage what is published.
//
//	GET  /v1/tenant/content-requests[?status=pending|all]
//	GET  /v1/tenant/content-requests/{id}             the review: permissions, scripts, MCP, the diff
//	POST /v1/tenant/content-requests/{id}/approve     {note?}
//	POST /v1/tenant/content-requests/{id}/decline     {note?}
//	GET  /v1/tenant/content-components                published content (any status)
//	POST /v1/tenant/content-components/{id}/unpublish
//	POST /v1/tenant/content-components/{id}/restore
//	DELETE /v1/tenant/content-components/{id}
//
// Approving publishes that version: the organization row (scope org, owner
// the author, source_id the author's component) is created or moved
// forward to it. Members keep using it until a newer request is approved.

// approvedVersion is the version an organization uses now of an author's
// component ("" when unpublished or never approved).
func (s *Service) approvedVersion(ctx context.Context, tenantID, kind, sourceID string) (string, error) {
	org, err := s.orgPublication(ctx, store.ContentComponent{ID: sourceID, Kind: kind, TenantID: tenantID})
	if err != nil || org == nil || org.Status != store.ContentStatusActive {
		return "", err
	}
	return org.LatestVersion, nil
}

func (s *Service) handleListContentRequests(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	q := store.ContentRequestQuery{TenantID: claims.Tenant, Status: store.ContentRequestPending}
	switch status := r.URL.Query().Get("status"); status {
	case "", string(store.ContentRequestPending):
	case "all":
		q.Status = ""
	default:
		httpx.Error(w, "status must be pending or all", http.StatusBadRequest)
		return
	}
	reqs, err := s.ContentComponents.ListContentRequests(ctx, q)
	if err != nil {
		obs.ServerError(w, "list publish requests failed", err)
		httpx.Error(w, "list publish requests failed", http.StatusInternalServerError)
		return
	}
	out := make([]wire.ContentPublishRequest, 0, len(reqs))
	for _, req := range reqs {
		display := req.Name
		if c, err := s.ContentComponents.GetContentComponent(ctx, req.ComponentID); err == nil {
			display = firstNonEmpty(c.DisplayName, c.Name)
		}
		approved, err := s.approvedVersion(ctx, req.TenantID, req.Kind, req.ComponentID)
		if err != nil {
			obs.ServerError(w, "list publish requests failed", err)
			httpx.Error(w, "list publish requests failed", http.StatusInternalServerError)
			return
		}
		out = append(out, publishRequestView(req, display, approved))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// orgRequest loads a request of the admin's organization.
func (s *Service) orgRequest(w http.ResponseWriter, r *http.Request, claims auth.Claims) (store.ContentPublishRequest, bool) {
	req, err := s.ContentComponents.GetContentRequest(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrContentRequestNotFound) || (err == nil && req.TenantID != claims.Tenant) {
		httpx.Error(w, "publish request not found", http.StatusNotFound)
		return store.ContentPublishRequest{}, false
	}
	if err != nil {
		obs.ServerError(w, "load publish request failed", err)
		httpx.Error(w, "load publish request failed", http.StatusInternalServerError)
		return store.ContentPublishRequest{}, false
	}
	return req, true
}

// requestedVersion loads the author's component and the version a request
// names; the author may have deleted it since (409).
func (s *Service) requestedVersion(w http.ResponseWriter, r *http.Request, req store.ContentPublishRequest) (store.ContentComponent, store.ContentComponentVersion, bool) {
	ctx := r.Context()
	c, err := s.ContentComponents.GetContentComponent(ctx, req.ComponentID)
	if err == nil && c.TenantID != req.TenantID {
		err = store.ErrContentNotFound
	}
	var v store.ContentComponentVersion
	if err == nil {
		v, err = s.ContentComponents.GetContentComponentVersion(ctx, c.ID, req.Version)
	}
	if errors.Is(err, store.ErrContentNotFound) {
		httpx.Error(w, "its author deleted this component or version", http.StatusConflict)
		return store.ContentComponent{}, store.ContentComponentVersion{}, false
	}
	if err != nil {
		obs.ServerError(w, "load requested version failed", err)
		httpx.Error(w, "load requested version failed", http.StatusInternalServerError)
		return store.ContentComponent{}, store.ContentComponentVersion{}, false
	}
	return c, v, true
}

// handleReviewContentRequest answers what an approver reviews: the
// requested version's declared permissions, requirements (MCP tools
// included), roster and skills; the files that execute; and the changes
// from the version the organization uses now.
func (s *Service) handleReviewContentRequest(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	req, ok := s.orgRequest(w, r, claims)
	if !ok {
		return
	}
	c, v, ok := s.requestedVersion(w, r, req)
	if !ok {
		return
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	view, err := s.reviewView(ctx, claims, c, v)
	if err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	out := wire.ContentReview{Component: view, ScriptFiles: pkg.ScriptFiles()}
	var prevPkg *contentpkg.Package
	org, err := s.orgPublication(ctx, c)
	if err != nil {
		obs.ServerError(w, "load organization publication failed", err)
		httpx.Error(w, "load organization publication failed", http.StatusInternalServerError)
		return
	}
	approved := ""
	if org != nil {
		if org.Status == store.ContentStatusActive {
			approved = org.LatestVersion
		}
		pv, err := s.ContentComponents.GetContentComponentVersion(ctx, org.ID, org.LatestVersion)
		if err != nil {
			contentStoreErr(w, "load published version failed", err)
			return
		}
		if prevPkg, err = s.contentPackage(ctx, pv); err != nil {
			obs.ServerError(w, "load published bundle failed", err)
			httpx.Error(w, "load published bundle failed", http.StatusBadGateway)
			return
		}
		prev, err := s.reviewView(ctx, claims, *org, pv)
		if err != nil {
			obs.ServerError(w, "decode content manifest failed", err)
			httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
			return
		}
		out.Previous = &prev
	}
	out.Request = publishRequestView(req, firstNonEmpty(c.DisplayName, c.Name), approved)
	out.Files = contentFileChanges(prevPkg, pkg)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// reviewView is a component at one version for an approver.
func (s *Service) reviewView(ctx context.Context, claims auth.Claims, c store.ContentComponent, v store.ContentComponentVersion) (wire.ContentComponent, error) {
	view, err := contentView(c, v, claims, nil, 0)
	if err != nil {
		return wire.ContentComponent{}, err
	}
	s.contentSkills(ctx, v, &view)
	return view, nil
}

// contentFileChanges lists the files that differ between two versions (all
// of cur's as added when prev is nil), with their text bounded like a
// version detail's.
func contentFileChanges(prev, cur *contentpkg.Package) []wire.ContentFileChange {
	files := func(p *contentpkg.Package) map[string][]byte {
		out := map[string][]byte{}
		if p == nil {
			return out
		}
		for _, path := range p.Bundle.Paths() {
			data, _ := p.Bundle.File(path)
			out[path] = data
		}
		return out
	}
	before, after := files(prev), files(cur)
	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	budget := maxContentViewBytes
	text := func(data []byte, ch *wire.ContentFileChange) string {
		if len(data) > maxContentFileView || len(data) > budget {
			ch.Truncated = true
			n := min(maxContentFileView, budget)
			for n > 0 && !utf8.Valid(data[:n]) {
				n--
			}
			budget -= n
			return string(data[:n])
		}
		budget -= len(data)
		return string(data)
	}
	out := []wire.ContentFileChange{}
	for _, p := range sorted {
		a, inA := before[p]
		b, inB := after[p]
		ch := wire.ContentFileChange{Path: p}
		switch {
		case !inA:
			ch.Change = wire.ContentFileAdded
		case !inB:
			ch.Change = wire.ContentFileRemoved
		case bytes.Equal(a, b):
			continue
		default:
			ch.Change = wire.ContentFileChanged
		}
		if (inA && !utf8.Valid(a)) || (inB && !utf8.Valid(b)) {
			ch.Binary = true
		} else {
			if inA {
				ch.Before = text(a, &ch)
			}
			if inB {
				ch.After = text(b, &ch)
			}
		}
		out = append(out, ch)
	}
	return out
}

// decisionNote reads an approve/decline body (optional).
func decisionNote(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, ok := readBody(w, r, 16<<10)
	if !ok {
		return "", false
	}
	var in wire.ContentDecisionInput
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			httpx.Error(w, "invalid json body", http.StatusBadRequest)
			return "", false
		}
	}
	return contentNote(w, in.Note)
}

// errStaleRequest: the organization already uses a newer version than the
// request names.
var errStaleRequest = errors.New("stale publish request")

// orgPublish publishes version v of the author's component src to its
// organization: the organization row is created, or moved forward to v,
// and made active.
func (s *Service) orgPublish(ctx context.Context, src store.ContentComponent, v store.ContentComponentVersion) (store.ContentComponent, error) {
	now := s.nowTime().UnixMilli()
	for attempt := 0; attempt < 2; attempt++ {
		org, err := s.orgPublication(ctx, src)
		if err != nil {
			return store.ContentComponent{}, err
		}
		if org == nil {
			row := store.ContentComponent{ID: ids.New("cc_"), Kind: src.Kind, Name: src.Name, TenantID: src.TenantID, Owner: src.Owner,
				Scope: store.ContentScopeOrg, Status: store.ContentStatusActive, SourceID: src.ID, CreatedAtMs: now}
			if err := contentshare.ListingFields(&row, v, now); err != nil {
				return store.ContentComponent{}, err
			}
			switch err := s.ContentComponents.CreateContentComponent(ctx, row, contentshare.CloneVersion(v, now)); {
			case errors.Is(err, store.ErrContentExists):
				continue // a concurrent approval created it
			case err != nil:
				return store.ContentComponent{}, err
			}
			row.LatestVersion = v.Version
			s.orgPublished(ctx, row, v)
			return row, nil
		}
		row := *org
		cmp, err := semver.Compare(v.Version, org.LatestVersion)
		if err != nil {
			return store.ContentComponent{}, err
		}
		if cmp > 0 {
			if err := contentshare.ListingFields(&row, v, now); err != nil {
				return store.ContentComponent{}, err
			}
			if err := s.ContentComponents.AddContentComponentVersion(ctx, row, org.LatestVersion, contentshare.CloneVersion(v, now)); err != nil {
				return store.ContentComponent{}, err
			}
			row.LatestVersion = v.Version
		} else if cmp < 0 {
			return store.ContentComponent{}, errStaleRequest
		}
		if row.Status != store.ContentStatusActive {
			if err := s.ContentComponents.SetContentComponentStatus(ctx, row.ID, store.ContentStatusActive, now); err != nil {
				return store.ContentComponent{}, err
			}
			row.Status = store.ContentStatusActive
		}
		s.orgPublished(ctx, row, v)
		return row, nil
	}
	return store.ContentComponent{}, fmt.Errorf("publish %s: organization copy changed concurrently", src.ID)
}

// decideContentRequest approves (publishing the version) or declines a
// pending request of the admin's organization, with a note for its author.
// The decision is claimed first (one decision per request); an approval
// whose publication then fails is reopened, never left approved with no
// effect.
func (s *Service) decideContentRequest(status store.ContentRequestStatus) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		s.decideContent(w, r, claims, status)
	}
}

func (s *Service) decideContent(w http.ResponseWriter, r *http.Request, claims auth.Claims, status store.ContentRequestStatus) {
	ctx := r.Context()
	req, ok := s.orgRequest(w, r, claims)
	if !ok {
		return
	}
	note, ok := decisionNote(w, r)
	if !ok {
		return
	}
	if req.Status != store.ContentRequestPending {
		httpx.Error(w, "this request was already "+string(req.Status), http.StatusConflict)
		return
	}
	var src store.ContentComponent
	var v store.ContentComponentVersion
	if status == store.ContentRequestApproved {
		if src, v, ok = s.requestedVersion(w, r, req); !ok {
			return
		}
		// An organization has one component of a kind and name: another
		// author's holds it, so this one is renamed, not approved.
		switch holder, err := s.orgNameHolder(ctx, src); {
		case err != nil:
			obs.ServerError(w, "check organization names failed", err)
			httpx.Error(w, "check organization names failed", http.StatusInternalServerError)
			return
		case holder != nil:
			httpx.Error(w, orgNameTaken(*holder, "decline this request and ask "+req.RequestedBy+" to rename theirs"), http.StatusConflict)
			return
		}
	}
	decided, err := s.ContentComponents.DecideContentRequest(ctx, store.ContentRequestDecision{
		RequestID: req.ID, TenantID: claims.Tenant, Status: status, By: claims.Subject, Note: note, AtMs: s.nowTime().UnixMilli(),
	})
	switch {
	case errors.Is(err, store.ErrContentRequestDecided):
		httpx.Error(w, "this request was already "+string(decided.Status), http.StatusConflict)
		return
	case errors.Is(err, store.ErrContentRequestNotFound):
		httpx.Error(w, "publish request not found", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "decide publish request failed", err)
		httpx.Error(w, "decide publish request failed", http.StatusInternalServerError)
		return
	}
	label := contentshare.KindLabel(req.Kind) + ` "` + req.Name + `"`
	approved := ""
	if status == store.ContentRequestApproved {
		label = contentshare.Label(src)
		org, err := s.orgPublish(ctx, src, v)
		if err != nil {
			_ = s.ContentComponents.ReopenContentRequest(ctx, req.ID)
			if errors.Is(err, errStaleRequest) {
				httpx.Error(w, "the organization already uses a newer version: ask the author for a new request", http.StatusConflict)
				return
			}
			if errors.Is(err, store.ErrContentOrgNameTaken) {
				httpx.Error(w, "the organization already has a "+contentshare.KindLabel(src.Kind)+" named "+src.Name+": decline this request and ask "+req.RequestedBy+" to rename theirs", http.StatusConflict)
				return
			}
			obs.ServerError(w, "publish to the organization failed", err)
			httpx.Error(w, "publish to the organization failed", http.StatusInternalServerError)
			return
		}
		approved = org.LatestVersion
		s.auditContent(ctx, "approve", claims.Subject, claims.Tenant, org, req.Version,
			label+" v"+req.Version+" of "+req.RequestedBy+" approved for the organization by "+claims.Subject+noteSuffix(note))
	} else {
		approved, _ = s.approvedVersion(ctx, req.TenantID, req.Kind, req.ComponentID)
		s.auditContent(ctx, "decline", claims.Subject, claims.Tenant, store.ContentComponent{Name: req.Name}, req.Version,
			label+" v"+req.Version+" of "+req.RequestedBy+" declined by "+claims.Subject+noteSuffix(note))
	}
	title := "Your " + label + " v" + req.Version + " is published to your organization"
	if status == store.ContentRequestDeclined {
		title = "Your " + label + " v" + req.Version + " was not approved for your organization"
	}
	text := note
	if text == "" {
		text = "Decided by " + claims.Subject + "."
	}
	s.notifyContent(ctx, claims.Tenant, notify.Event{
		Type: wire.NotificationContentReview, Recipients: []string{req.RequestedBy}, Actor: claims.Subject,
		Title: title, Text: text, URL: s.appLink(r, "library"), DedupKey: "content-decision:" + req.ID,
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"request": publishRequestView(decided, firstNonEmpty(src.DisplayName, req.Name), approved)})
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}

// handleListOrgContent lists the organization's published content (any
// status) for its admins.
func (s *Service) handleListOrgContent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	comps, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{TenantID: claims.Tenant, Scopes: []store.ContentScope{store.ContentScopeOrg}})
	if err != nil {
		obs.ServerError(w, "list organization content failed", err)
		httpx.Error(w, "list organization content failed", http.StatusInternalServerError)
		return
	}
	s.writeAdminContent(w, r, claims, comps)
}

// writeAdminContent answers published components for an admin.
func (s *Service) writeAdminContent(w http.ResponseWriter, r *http.Request, claims auth.Claims, comps []store.ContentComponent) {
	views, err := s.contentViews(r.Context(), claims, comps, nil)
	if err != nil {
		obs.ServerError(w, "load published content failed", err)
		httpx.Error(w, "load published content failed", http.StatusInternalServerError)
		return
	}
	byID := map[string]store.ContentComponent{}
	for _, c := range comps {
		byID[c.ID] = c
	}
	sortContentViews(views)
	out := make([]wire.ContentAdminComponent, 0, len(views))
	for _, v := range views {
		c := byID[v.ID]
		out = append(out, wire.ContentAdminComponent{Component: v, TenantID: c.TenantID, EnabledTenants: c.EnabledTenants})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"components": out})
}

// orgContent loads an organization row of the admin's organization.
func (s *Service) orgContent(w http.ResponseWriter, r *http.Request, claims auth.Claims) (store.ContentComponent, bool) {
	c, err := s.ContentComponents.GetContentComponent(r.Context(), r.PathValue("id"))
	if err == nil && (c.Scope != store.ContentScopeOrg || c.TenantID != claims.Tenant) {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return store.ContentComponent{}, false
	}
	return c, true
}

// handleOrgContentStatus unpublishes (retired: hidden from the Library and
// new use; rooms that pinned it keep their version) or restores an
// organization component.
func (s *Service) handleOrgContentStatus(status store.ContentStatus) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		c, ok := s.orgContent(w, r, claims)
		if !ok {
			return
		}
		s.setContentStatus(w, r, claims, c, status)
	}
}

// setContentStatus changes a published component's status and audits it.
func (s *Service) setContentStatus(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent, status store.ContentStatus) {
	if err := s.ContentComponents.SetContentComponentStatus(r.Context(), c.ID, status, s.nowTime().UnixMilli()); err != nil {
		contentStoreErr(w, "change content status failed", err)
		return
	}
	action, verb := "unpublish", "unpublished"
	if status == store.ContentStatusActive {
		action, verb = "restore", "restored"
	}
	s.auditContent(r.Context(), action, claims.Subject, c.TenantID, c, c.LatestVersion, contentshare.Label(c)+" "+verb+" by "+claims.Subject)
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveOrgContent deletes an organization component with its
// versions and uses (rooms lose it). The author keeps their own.
func (s *Service) handleRemoveOrgContent(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	c, ok := s.orgContent(w, r, claims)
	if !ok {
		return
	}
	s.removeContent(w, r, claims, c)
}

// removeContent deletes a published component and audits it.
func (s *Service) removeContent(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent) {
	if err := s.ContentComponents.DeleteContentComponent(r.Context(), c.ID); err != nil {
		contentStoreErr(w, "remove content component failed", err)
		return
	}
	s.auditContent(r.Context(), "remove", claims.Subject, c.TenantID, c, c.LatestVersion, contentshare.Label(c)+" removed by "+claims.Subject)
	w.WriteHeader(http.StatusNoContent)
}
