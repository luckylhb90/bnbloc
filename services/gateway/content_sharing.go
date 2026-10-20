package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Sharing and publishing content components (docs/prd/2026-10-12-experts-
// and-teams.md §1, step 4), the author's side:
//
//	POST /v1/content-components/{id}/shares   {members?, roomId?, version?, note?}
//	GET  /v1/content-components/{id}/shares   the copies made from it and its room shares
//	DELETE /v1/content-components/{id}/shares/rooms/{room}   revoke a room share (copies stay)
//	POST /v1/content-components/{id}/update   a shared copy moves to the author's latest
//	POST /v1/content-components/{id}/publish  {scope: org|personal|platform, version?, note?}
//
// A shared copy is a component owned by the recipient (scope shared,
// source_id = the author's component, shared_by = the author) whose
// versions are the author's version rows cloned: same manifest, same
// content-addressed bundle. It is pinned to what it was given; the author's
// later edits reach it only when the recipient updates it. Only the author
// of a personal component shares or publishes it. Organization publication
// is a request an org admin decides (content_review.go), or immediate where
// the organization requires no approval (publishToOrg); platform scope is
// a super admin's signed install (content_platform.go).

// Share and publish bounds.
const (
	// maxShareRecipients bounds one share (named members plus a room's).
	maxShareRecipients  = 100
	maxContentNoteRunes = 500
)

// contentNote trims a request note and checks its bound.
func contentNote(w http.ResponseWriter, note string) (string, bool) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > maxContentNoteRunes {
		httpx.Error(w, "the note must be at most 500 characters", http.StatusBadRequest)
		return "", false
	}
	return note, true
}

// auditContent appends a content lifecycle row (kind content).
// Best-effort, like the other content audits.
func (s *Service) auditContent(ctx context.Context, action, actor, tenantID string, c store.ContentComponent, version, detail string) {
	s.audit(ctx, tenantID, store.ExtAuditRecord{
		Kind: wire.AuditKindContent, Name: c.Name + "@" + version, Action: action, Actor: actor,
		Outcome: wire.AuditOutcomeAllow, Detail: detail,
	})
}

// notifyContent hands a room-less content notification to the emitter. It
// returns at once: the enqueue runs detached and best-effort, so the request
// never waits on (or fails because of) notification work.
func (s *Service) notifyContent(ctx context.Context, tenantID string, ev notify.Event) {
	if s.Alerts == nil || len(ev.Recipients) == 0 {
		return
	}
	s.goDetached(ctx, tenantID, 10*time.Second, func(ctx context.Context) {
		if _, err := s.Alerts.Emit(ctx, ev); err != nil {
			slog.Warn("bnbloc gateway: content notification enqueue failed",
				"event", "bnbloc.notify.enqueue_failed", "type", ev.Type, "error", err)
		}
	})
}

// appLink is a deep link into the web app ("#/library", "#/org").
func (s *Service) appLink(r *http.Request, route string) string {
	return s.publicOrigin(r) + "/#/" + route
}

// orgMember reports whether subject belongs to the organization (fail
// closed without a tenant directory).
func (s *Service) orgMember(tenantID, subject string) bool {
	_, err := s.memberRole(tenantID, subject)
	return err == nil
}

// ---------------------------------------------------------------- shares

func (s *Service) handleShareContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	src, err := s.ownContent(ctx, claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	raw, ok := readBody(w, r, 64<<10)
	if !ok {
		return
	}
	var req wire.ContentShareRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	note, ok := contentNote(w, req.Note)
	if !ok {
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, src.ID, firstNonEmpty(req.Version, src.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	recipients, skipped, ok := s.shareRecipients(w, r, claims, req)
	if !ok {
		return
	}
	out := wire.ContentShareReply{Version: v.Version, Shared: []wire.ContentShareResult{}, Skipped: skipped}
	if room := strings.TrimSpace(req.RoomID); room != "" {
		// The room share stands: members who join later get a copy too
		// (realtime's member-join step), until the author revokes it.
		if err := contentshare.RecordRoomShare(ctx, s.ContentComponents, src, v, room, note, s.nowTime().UnixMilli()); err != nil {
			obs.ServerError(w, "record room share failed", err)
			httpx.Error(w, "record room share failed", http.StatusInternalServerError)
			return
		}
		out.Room = room
	}
	var reached []string
	for _, member := range recipients {
		res, err := contentshare.Copy(ctx, s.ContentComponents, src, v, member, s.nowTime().UnixMilli())
		if err != nil {
			obs.ServerError(w, "share content component failed", err)
			httpx.Error(w, "share content component failed", http.StatusInternalServerError)
			return
		}
		out.Shared = append(out.Shared, res)
		if res.Status != wire.ContentShareUnchanged {
			reached = append(reached, member)
		}
	}
	if len(out.Shared) > 0 {
		names := make([]string, 0, len(out.Shared))
		for _, res := range out.Shared {
			names = append(names, res.Member+" ("+res.Status+")")
		}
		detail := contentshare.Label(src) + " v" + v.Version + " shared by " + claims.Subject + " with " + strings.Join(names, ", ")
		if req.RoomID != "" {
			detail += " (room " + strings.TrimSpace(req.RoomID) + ")"
		}
		s.auditContent(ctx, "share", claims.Subject, claims.Tenant, src, v.Version, detail)
	}
	for _, ev := range contentshare.Announce(contentshare.Share{
		Source: src, Version: v.Version, SharedBy: claims.Subject, Note: note, Link: s.appLink(r, "library"),
	}, reached) {
		s.notifyContent(ctx, claims.Tenant, ev)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// shareRecipients resolves a share's recipients: the named members and the
// human members of the room, each once, never the author, only members of
// the author's organization. It answers refusals itself.
func (s *Service) shareRecipients(w http.ResponseWriter, r *http.Request, claims auth.Claims, req wire.ContentShareRequest) ([]string, []wire.ContentShareSkip, bool) {
	candidates := append([]string(nil), req.Members...)
	if room := strings.TrimSpace(req.RoomID); room != "" {
		if m, ok := s.roomMember(claims.Subject, room); !ok || !m.IsHuman() {
			httpx.Error(w, "you can share only with a room you are a member of", http.StatusForbidden)
			return nil, nil, false
		}
		if !s.roomInTenant(claims, room) {
			httpx.Error(w, "that room belongs to another organization", http.StatusForbidden)
			return nil, nil, false
		}
		if s.RoomMembers == nil {
			obs.ServerError(w, "room members unavailable", nil)
			httpx.Error(w, "room members unavailable", http.StatusServiceUnavailable)
			return nil, nil, false
		}
		members, err := s.RoomMembers(tenant.NewContext(r.Context(), claims.Tenant), room)
		if err != nil {
			obs.ServerError(w, "list room members failed", err)
			httpx.Error(w, "list room members failed", http.StatusBadGateway)
			return nil, nil, false
		}
		for _, m := range members {
			candidates = append(candidates, m.UserID)
		}
	}
	seen := map[string]bool{}
	var recipients []string
	skipped := []wire.ContentShareSkip{}
	for _, m := range candidates {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		switch {
		case m == claims.Subject:
			if req.RoomID == "" {
				skipped = append(skipped, wire.ContentShareSkip{Member: m, Reason: "that is you"})
			}
		case !s.orgMember(claims.Tenant, m):
			skipped = append(skipped, wire.ContentShareSkip{Member: m, Reason: "not a member of your organization"})
		default:
			recipients = append(recipients, m)
		}
	}
	if len(recipients) > maxShareRecipients {
		httpx.Error(w, fmt.Sprintf("a share reaches at most %d members", maxShareRecipients), http.StatusBadRequest)
		return nil, nil, false
	}
	if len(recipients) == 0 && len(skipped) == 0 {
		httpx.Error(w, "name the members or the room to share with", http.StatusBadRequest)
		return nil, nil, false
	}
	return recipients, skipped, true
}

// handleListContentShares lists the copies of the caller's component.
func (s *Service) handleListContentShares(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	src, err := s.ownContent(ctx, claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	copies, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{
		TenantID: src.TenantID, Kind: src.Kind, Scopes: []store.ContentScope{store.ContentScopeShared}, SourceID: src.ID,
	})
	if err != nil {
		obs.ServerError(w, "list shared copies failed", err)
		httpx.Error(w, "list shared copies failed", http.StatusInternalServerError)
		return
	}
	out := make([]wire.ContentCopy, 0, len(copies))
	for _, c := range copies {
		out = append(out, wire.ContentCopy{Member: c.Owner, ComponentID: c.ID, Version: c.LatestVersion, UpdatedAtMs: c.UpdatedAtMs})
	}
	shares, err := s.ContentComponents.ListContentRoomShares(ctx, store.ContentRoomShareQuery{ComponentID: src.ID})
	if err != nil {
		obs.ServerError(w, "list room shares failed", err)
		httpx.Error(w, "list room shares failed", http.StatusInternalServerError)
		return
	}
	rooms := make([]wire.ContentRoomShare, 0, len(shares))
	for _, sh := range shares {
		rooms = append(rooms, wire.ContentRoomShare{RoomID: sh.RoomID, Version: sh.Version, Note: sh.Note, CreatedAtMs: sh.CreatedAtMs, UpdatedAtMs: sh.UpdatedAtMs})
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ContentShares{Copies: out, Rooms: rooms})
}

// handleRevokeRoomShare ends the author's room share: members who join the
// room later get no copy; the copies already given stay theirs.
func (s *Service) handleRevokeRoomShare(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	src, err := s.ownContent(ctx, claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	room := r.PathValue("room")
	removed, err := s.ContentComponents.DeleteContentRoomShare(ctx, room, src.ID)
	if err != nil {
		obs.ServerError(w, "revoke room share failed", err)
		httpx.Error(w, "revoke room share failed", http.StatusInternalServerError)
		return
	}
	if !removed {
		httpx.Error(w, "this component is not shared with that room", http.StatusNotFound)
		return
	}
	s.auditContent(ctx, "unshare", claims.Subject, claims.Tenant, src, src.LatestVersion,
		contentshare.Label(src)+" room share with "+room+" revoked by "+claims.Subject+" (copies already given stay)")
	w.WriteHeader(http.StatusNoContent)
}

// sourceOfCopy loads the author's component a shared copy came from, while
// it is still the author's personal component (nil when deleted).
func (s *Service) sourceOfCopy(ctx context.Context, cp store.ContentComponent) (*store.ContentComponent, error) {
	src, err := s.ContentComponents.GetContentComponent(ctx, cp.SourceID)
	if errors.Is(err, store.ErrContentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if src.Scope != store.ContentScopePersonal || src.Owner != cp.SharedBy || src.TenantID != cp.TenantID {
		return nil, nil
	}
	return &src, nil
}

// handleUpdateContentCopy moves the caller's shared copy to its author's
// latest version (an explicit step: the author's edits never reach a copy
// by themselves).
func (s *Service) handleUpdateContentCopy(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	ctx := r.Context()
	cp, err := s.heldContent(ctx, claims, r.PathValue("id"))
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if cp.Scope != store.ContentScopeShared {
		httpx.Error(w, "only a copy someone shared with you is updated from its author", http.StatusConflict)
		return
	}
	src, err := s.sourceOfCopy(ctx, cp)
	if err != nil {
		obs.ServerError(w, "load the author's component failed", err)
		httpx.Error(w, "load the author's component failed", http.StatusInternalServerError)
		return
	}
	if src == nil {
		httpx.Error(w, "its author no longer has it: your copy stays as it is", http.StatusConflict)
		return
	}
	if cmp, err := semver.Compare(src.LatestVersion, cp.LatestVersion); err != nil || cmp <= 0 {
		s.writeContentDetail(w, r, claims, cp, http.StatusOK)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, src.ID, src.LatestVersion)
	if err != nil {
		contentStoreErr(w, "load the author's version failed", err)
		return
	}
	now := s.nowTime().UnixMilli()
	next := cp
	if err := contentshare.ListingFields(&next, v, now); err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	switch err := s.ContentComponents.AddContentComponentVersion(ctx, next, cp.LatestVersion, contentshare.CloneVersion(v, now)); {
	case errors.Is(err, store.ErrContentVersionConflict):
		httpx.Error(w, "the copy changed while updating: reload and try again", http.StatusConflict)
		return
	case err != nil:
		contentStoreErr(w, "update shared copy failed", err)
		return
	}
	next.LatestVersion = v.Version
	s.auditContent(ctx, "update", claims.Subject, claims.Tenant, next, v.Version,
		contentshare.Label(next)+" copy of "+claims.Subject+" updated to "+cp.SharedBy+"'s v"+v.Version)
	s.writeContentDetail(w, r, claims, next, http.StatusOK)
}

// ---------------------------------------------------------------- publish

// errPlatformPublish answers a member asking for platform scope.
const errPlatformPublish = "publishing to every organization is a platform admin action: they export it as a package, sign it and install it (bnbloc content promote). Ask a platform admin; to reach your organization, publish it with scope org"

// errJobWidens is the one rule that a job's grant never widens who sees
// content: an agent acting for a member never takes their component to
// the organization, neither by publishing it nor by a saved version's
// resubmission. Sharing beyond its author is the member's own click (the
// room tools put a card in the room that only they see,
// docs/prd/2026-10-20-room-setup-by-conversation.md §2.3), so text an
// agent read can never publish for them. publishToOrg, the one way to the
// organization, answers it.
var errJobWidens = errors.New("publishing to the organization is the member's own click: publish_component asks them in the room")

// handlePublishContent changes who can use the caller's component: org
// opens a publish request, personal takes it out of the organization,
// platform is a super admin's action. The room tools' publish_component
// calls it with a job grant, acting as the job's requester, to withdraw
// only (errJobWidens).
func (s *Service) handlePublishContent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
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
	raw, ok := readBody(w, r, 16<<10)
	if !ok {
		return
	}
	var in wire.ContentPublishInput
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	note, ok := contentNote(w, in.Note)
	if !ok {
		return
	}
	switch in.Scope {
	case wire.ContentScopeOrg:
		s.requestOrgPublication(w, r, actor, c, strings.TrimSpace(in.Version), note)
	case wire.ContentScopePersonal:
		s.withdrawOrgPublication(w, r, claims, c)
	case wire.ContentScopePlatform:
		httpx.Error(w, errPlatformPublish, http.StatusForbidden)
	default:
		httpx.Error(w, "scope must be personal, org or platform", http.StatusBadRequest)
	}
}

// orgPublication is the organization copy of an author's component (nil
// before a first approval).
func (s *Service) orgPublication(ctx context.Context, src store.ContentComponent) (*store.ContentComponent, error) {
	rows, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{
		TenantID: src.TenantID, Kind: src.Kind, Scopes: []store.ContentScope{store.ContentScopeOrg}, SourceID: src.ID, Limit: 1,
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (s *Service) requestOrgPublication(w http.ResponseWriter, r *http.Request, actor contentActor, c store.ContentComponent, version, note string) {
	ctx := r.Context()
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(version, c.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	org, err := s.orgPublication(ctx, c)
	if err != nil {
		obs.ServerError(w, "load organization publication failed", err)
		httpx.Error(w, "load organization publication failed", http.StatusInternalServerError)
		return
	}
	if org != nil && org.Status == store.ContentStatusActive && org.LatestVersion == v.Version {
		httpx.Error(w, fmt.Sprintf("version %s is already published to your organization", v.Version), http.StatusConflict)
		return
	}
	switch holder, err := s.orgNameHolder(ctx, c); {
	case err != nil:
		obs.ServerError(w, "check organization names failed", err)
		httpx.Error(w, "check organization names failed", http.StatusInternalServerError)
		return
	case holder != nil:
		httpx.Error(w, orgNameTaken(*holder, "rename yours: save it under a new name, and ask again"), http.StatusConflict)
		return
	}
	res, err := s.publishToOrg(ctx, r, actor, c, v, note, org, true)
	switch {
	case errors.Is(err, errJobWidens):
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return
	case errors.Is(err, errStaleRequest):
		httpx.Error(w, "the organization already uses a newer version: publish that one or a newer one", http.StatusConflict)
		return
	case errors.Is(err, store.ErrContentOrgNameTaken):
		httpx.Error(w, "the organization already has a "+contentshare.KindLabel(c.Kind)+" named "+c.Name+": rename yours: save it under a new name, and publish again", http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "publish to the organization failed", err)
		httpx.Error(w, "publish to the organization failed", http.StatusInternalServerError)
		return
	}
	if res.request == nil {
		httpx.WriteJSON(w, http.StatusCreated, wire.ContentPublishReply{Scope: wire.ContentScopeOrg,
			Note: "Published to your organization: its members can use version " + res.published + " now (your organization publishes without approval)."})
		return
	}
	msg := "Waiting for an organization admin's approval."
	if approved := res.request.ApprovedVersion; approved != "" {
		msg += " Members keep using version " + approved + " until then."
	}
	httpx.WriteJSON(w, http.StatusCreated, wire.ContentPublishReply{Scope: wire.ContentScopePersonal, Request: res.request, Note: msg})
}

// orgPublishResult is what a publish to the organization did: opened a
// request for an org admin, or published version published at once; or,
// for a version a job saved, proposed: the version only its author's
// click sends (errJobWidens).
type orgPublishResult struct {
	request   *wire.ContentPublishRequest
	published string
	proposed  string
}

// publishToOrg is the ONE way a member's publish reaches the organization:
// the publish route (a member's click, a publish_component card's too) and
// a new version's resubmission call it, for every content kind. The organization's
// settings decide through contentshare.NeedsApproval: by default it opens
// a request an org admin approves (openPublishRequest, notifying the
// admins when notifyAdmins); where the organization requires no approval
// it publishes v at once (orgPublish), withdraws any pending request it
// supersedes and audits the publication, which admins can still
// unpublish, restore or remove. org is c's organization row (nil before a
// first publication); the caller has checked the organization's name. A
// job's grant never gets here (errJobWidens).
func (s *Service) publishToOrg(ctx context.Context, r *http.Request, actor contentActor, c store.ContentComponent, v store.ContentComponentVersion, note string, org *store.ContentComponent, notifyAdmins bool) (orgPublishResult, error) {
	if actor.Job != nil {
		return orgPublishResult{}, errJobWidens
	}
	settings, err := s.tenantSettings(ctx, c.TenantID)
	if err != nil {
		return orgPublishResult{}, err
	}
	if contentshare.NeedsApproval(settings) {
		view, err := s.openPublishRequest(ctx, r, actor, c, v.Version, note, org, notifyAdmins)
		if err != nil {
			return orgPublishResult{}, err
		}
		return orgPublishResult{request: &view}, nil
	}
	claims := actor.Claims
	row, err := s.orgPublish(ctx, c, v)
	if err != nil {
		return orgPublishResult{}, err
	}
	if err := s.withdrawPending(ctx, c, claims.Subject); err != nil {
		return orgPublishResult{}, err
	}
	s.contentSaved(ctx, actor, c, v.Version, wire.ContentSavedPublished, false)
	detail := contentshare.Label(c) + " v" + v.Version + " published to the organization by " + claims.Subject + " (no approval required)"
	if actor.Job != nil {
		detail += " (through agent " + actor.Job.OwnerUserID + "/" + actor.Job.Provider + ", job " + actor.Job.JobID + ")"
	}
	s.auditContent(ctx, "publish", claims.Subject, claims.Tenant, row, v.Version, detail+noteSuffix(note))
	return orgPublishResult{published: row.LatestVersion}, nil
}

// tenantSettings reads an organization's settings; without a registry the
// defaults (approval required).
func (s *Service) tenantSettings(ctx context.Context, tenantID string) (store.Tenant, error) {
	if s.TenantSettings == nil {
		return store.Tenant{TenantID: tenantID, PublishApproval: wire.PublishApprovalRequired}, nil
	}
	return s.TenantSettings(ctx, tenantID)
}

// withdrawPending withdraws c's pending publish requests (by its author:
// a withdrawal, or a direct publication that supersedes them).
func (s *Service) withdrawPending(ctx context.Context, c store.ContentComponent, by string) error {
	pending, err := s.ContentComponents.ListContentRequests(ctx, store.ContentRequestQuery{ComponentID: c.ID, Status: store.ContentRequestPending})
	if err != nil {
		return err
	}
	for _, p := range pending {
		_, err := s.ContentComponents.DecideContentRequest(ctx, store.ContentRequestDecision{RequestID: p.ID, TenantID: p.TenantID,
			Status: store.ContentRequestWithdrawn, By: by, AtMs: s.nowTime().UnixMilli()})
		if err != nil && !errors.Is(err, store.ErrContentRequestDecided) {
			return err
		}
	}
	return nil
}

// openPublishRequest stores a pending request for version of the author's
// component c (superseding a pending one), audits it and, when notify,
// tells the organization's admins. org is c's organization row (nil before
// a first approval).
func (s *Service) openPublishRequest(ctx context.Context, r *http.Request, actor contentActor, c store.ContentComponent, version, note string, org *store.ContentComponent, notifyAdmins bool) (wire.ContentPublishRequest, error) {
	claims := actor.Claims
	req, err := s.ContentComponents.CreateContentRequest(ctx, store.ContentPublishRequest{
		ID: ids.New("cpr_"), TenantID: claims.Tenant, ComponentID: c.ID, Kind: c.Kind, Name: c.Name, Version: version,
		RequestedBy: claims.Subject, Note: note, CreatedAtMs: s.nowTime().UnixMilli(),
	})
	if err != nil {
		return wire.ContentPublishRequest{}, err
	}
	s.contentSaved(ctx, actor, c, version, wire.ContentSavedSubmitted, false)
	detail := contentshare.Label(c) + " v" + version + " submitted for the organization by " + claims.Subject
	if actor.Job != nil {
		detail += " (through agent " + actor.Job.OwnerUserID + "/" + actor.Job.Provider + ", job " + actor.Job.JobID + ")"
	}
	s.auditContent(ctx, "request", claims.Subject, claims.Tenant, c, version, detail)
	if notifyAdmins {
		text := note
		if text == "" {
			text = "Review its permissions and changes in the Organization console."
		}
		var admins []string
		if s.OrgAdmins != nil {
			var err error
			if admins, err = s.OrgAdmins(claims.Tenant); err != nil {
				// Fail-open by design: the request is stored and shows in
				// the Organization console's review queue, so only its
				// notification is lost; refusing the request over a
				// notification read would lose the member's submission.
				slog.Warn("bnbloc gateway: org admins unreadable; review notification skipped",
					"event", "bnbloc.content.notify_failed", "request", req.ID, "error", err)
			}
		}
		s.notifyContent(ctx, claims.Tenant, notify.Event{
			Type: wire.NotificationContentReview, Recipients: admins, Actor: claims.Subject,
			Title: claims.Subject + " asks to publish the " + contentshare.Label(c) + " v" + version + " to the organization",
			Text:  text, URL: s.appLink(r, "org"), DedupKey: "content-request:" + req.ID,
		})
	}
	approved := ""
	if org != nil && org.Status == store.ContentStatusActive {
		approved = org.LatestVersion
	}
	return publishRequestView(req, firstNonEmpty(c.DisplayName, c.Name), approved), nil
}

// resubmitOrgPublication publishes a new version the author just saved to
// the organization (publishToOrg: a request, or at once where the
// organization requires no approval; a version a job saved is only
// proposed, errJobWidens) when their component is in use by
// the organization (an active org row) or has a request pending ("Gap
// closure" 5): one pending request per component (the new one supersedes
// it), and the admins are notified only when none was pending, so repeated
// saves do not repeat the notification. Best-effort: the save already
// succeeded, so a failure is logged and the author can still publish by
// hand.
func (s *Service) resubmitOrgPublication(ctx context.Context, r *http.Request, actor contentActor, c store.ContentComponent, version string) orgPublishResult {
	if c.Scope != store.ContentScopePersonal || !ownedBy(c, actor.Claims) {
		return orgPublishResult{}
	}
	org, err := s.orgPublication(ctx, c)
	if err != nil {
		slog.Warn("bnbloc gateway: resubmit to the organization failed", "event", "bnbloc.content.resubmit_failed", "component", c.ID, "error", err)
		return orgPublishResult{}
	}
	pending, err := s.ContentComponents.ListContentRequests(ctx, store.ContentRequestQuery{ComponentID: c.ID, Status: store.ContentRequestPending, Limit: 1})
	if err != nil {
		slog.Warn("bnbloc gateway: resubmit to the organization failed", "event", "bnbloc.content.resubmit_failed", "component", c.ID, "error", err)
		return orgPublishResult{}
	}
	if (org == nil || org.Status != store.ContentStatusActive) && len(pending) == 0 {
		return orgPublishResult{}
	}
	if holder, err := s.orgNameHolder(ctx, c); err != nil || holder != nil {
		return orgPublishResult{} // the name is another author's: the request could not be approved
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, version)
	if err != nil {
		slog.Warn("bnbloc gateway: resubmit to the organization failed", "event", "bnbloc.content.resubmit_failed", "component", c.ID, "error", err)
		return orgPublishResult{}
	}
	note := "Opened automatically for the new version " + version + "."
	if len(pending) > 0 {
		note = "Opened automatically for the new version " + version + " (replaces the request for " + pending[0].Version + ")."
	}
	res, err := s.publishToOrg(ctx, r, actor, c, v, note, org, len(pending) == 0)
	if errors.Is(err, errJobWidens) {
		// Saved through a job's grant: the version stays the author's until
		// they publish it (the room tools offer them the card).
		return orgPublishResult{proposed: v.Version}
	}
	if err != nil {
		slog.Warn("bnbloc gateway: resubmit to the organization failed", "event", "bnbloc.content.resubmit_failed", "component", c.ID, "error", err)
		return orgPublishResult{}
	}
	return res
}

// orgNameHolder is the organization row of another component that holds
// c's name in its organization (nil when the name is free or c's own): an
// organization has one component of a kind and name ("Gap closure" 4).
func (s *Service) orgNameHolder(ctx context.Context, c store.ContentComponent) (*store.ContentComponent, error) {
	rows, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{
		TenantID: c.TenantID, Kind: c.Kind, Name: c.Name, Scopes: []store.ContentScope{store.ContentScopeOrg},
	})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].SourceID != c.ID {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// orgNameTaken says that holder holds the name, and what to do.
func orgNameTaken(holder store.ContentComponent, then string) string {
	state := ""
	if holder.Status == store.ContentStatusRetired {
		state = ", unpublished"
	}
	return fmt.Sprintf("the organization already has a %s named %s (by %s%s): %s", contentshare.KindLabel(holder.Kind), holder.Name, holder.Owner, state, then)
}

// withdrawOrgPublication takes the author's component out of the
// organization: a pending request is withdrawn and the organization copy
// unpublished (rooms that pinned it keep their version).
func (s *Service) withdrawOrgPublication(w http.ResponseWriter, r *http.Request, claims auth.Claims, c store.ContentComponent) {
	ctx := r.Context()
	now := s.nowTime().UnixMilli()
	if err := s.withdrawPending(ctx, c, claims.Subject); err != nil {
		obs.ServerError(w, "withdraw publish request failed", err)
		httpx.Error(w, "withdraw publish request failed", http.StatusInternalServerError)
		return
	}
	org, err := s.orgPublication(ctx, c)
	if err != nil {
		obs.ServerError(w, "load organization publication failed", err)
		httpx.Error(w, "load organization publication failed", http.StatusInternalServerError)
		return
	}
	if org != nil && org.Status != store.ContentStatusRetired {
		if err := s.ContentComponents.SetContentComponentStatus(ctx, org.ID, store.ContentStatusRetired, now); err != nil {
			contentStoreErr(w, "unpublish content component failed", err)
			return
		}
		s.auditContent(ctx, "unpublish", claims.Subject, claims.Tenant, *org, org.LatestVersion,
			contentshare.Label(*org)+" taken out of the organization by its author "+claims.Subject)
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ContentPublishReply{Scope: wire.ContentScopePersonal, Note: "It is personal: only its author uses it (copies shared with members stay theirs)."})
}

// publishRequestView is a request on the wire.
func publishRequestView(r store.ContentPublishRequest, displayName, approved string) wire.ContentPublishRequest {
	return wire.ContentPublishRequest{
		ID: r.ID, ComponentID: r.ComponentID, Kind: shortKind(r.Kind), Name: r.Name, DisplayName: displayName, Version: r.Version,
		RequestedBy: r.RequestedBy, Note: r.Note, Status: string(r.Status), DecidedBy: r.DecidedBy, DecisionNote: r.DecisionNote,
		DecidedAtMs: r.DecidedAtMs, CreatedAtMs: r.CreatedAtMs, ApprovedVersion: approved,
	}
}

// applyContentSharing fills the sharing fields of views: a held copy's
// newer author version, and an author's organization publication and
// latest request (two queries for the whole listing).
func (s *Service) applyContentSharing(ctx context.Context, claims auth.Claims, comps []store.ContentComponent, views []wire.ContentComponent) error {
	byID := make(map[string]store.ContentComponent, len(comps))
	authored := false
	for _, c := range comps {
		byID[c.ID] = c
		authored = authored || (c.Scope == store.ContentScopePersonal && ownedBy(c, claims))
	}
	var orgBySource map[string]store.ContentComponent
	var requestBySource map[string]store.ContentPublishRequest
	if authored {
		orgRows, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{
			TenantID: claims.Tenant, Owner: claims.Subject, Scopes: []store.ContentScope{store.ContentScopeOrg},
		})
		if err != nil {
			return err
		}
		orgBySource = map[string]store.ContentComponent{}
		for _, o := range orgRows {
			orgBySource[o.SourceID] = o
		}
		reqs, err := s.ContentComponents.ListContentRequests(ctx, store.ContentRequestQuery{TenantID: claims.Tenant, RequestedBy: claims.Subject, Limit: store.MaxContentList})
		if err != nil {
			return err
		}
		requestBySource = map[string]store.ContentPublishRequest{}
		for _, q := range reqs { // newest first: keep the first per component
			kept, ok := requestBySource[q.ComponentID]
			// A request superseded in the same millisecond (a save's
			// resubmission, then an explicit one) is not the latest.
			if !ok || (kept.CreatedAtMs == q.CreatedAtMs && kept.Status == store.ContentRequestWithdrawn && q.Status != store.ContentRequestWithdrawn) {
				requestBySource[q.ComponentID] = q
			}
		}
	}
	for i := range views {
		c, ok := byID[views[i].ID]
		if !ok {
			continue
		}
		switch {
		case c.Scope == store.ContentScopeShared && views[i].Removable:
			src, err := s.sourceOfCopy(ctx, c)
			if err != nil {
				return err
			}
			if src != nil {
				if cmp, err := semver.Compare(src.LatestVersion, c.LatestVersion); err == nil && cmp > 0 {
					views[i].SourceLatestVersion = src.LatestVersion
				}
			}
		case views[i].Mine:
			org, hasOrg := orgBySource[c.ID]
			req, hasReq := requestBySource[c.ID]
			if !hasOrg && !hasReq {
				continue
			}
			pub := &wire.ContentPublication{}
			approved := ""
			if hasOrg {
				pub.OrgComponentID, pub.OrgVersion, pub.OrgStatus = org.ID, org.LatestVersion, string(org.Status)
				if org.Status == store.ContentStatusActive {
					approved = org.LatestVersion
				}
			}
			if hasReq {
				rv := publishRequestView(req, firstNonEmpty(c.DisplayName, c.Name), approved)
				pub.Request = &rv
			}
			views[i].Publication = pub
		}
	}
	return nil
}
