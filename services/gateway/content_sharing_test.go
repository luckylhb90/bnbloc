package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// sharingGateway is the content gateway plus the organization around it:
// ada (org admin of home), sam (super admin), eve (org admin of another
// organization) and kim (a member there); home's members; room-1's members
// for room shares; an audit store; a notifier so notifications queue.
type sharingGateway struct {
	*contentGateway
	audit  *store.MemoryExtensionAudit
	outbox *store.MemoryNotifications
}

func newSharingGateway(t *testing.T) *sharingGateway {
	t.Helper()
	g := newContentGateway(t)
	roles := map[string][2]string{
		"ada": {"home", string(rbac.RoleAdmin)}, "sam": {"home", string(rbac.RoleSuperAdmin)},
		"eve": {"other", string(rbac.RoleAdmin)}, "kim": {"other", string(rbac.RoleMember)},
	}
	prev := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, def string) (string, string, error) {
		if r, ok := roles[id.Subject]; ok {
			return r[0], r[1], nil
		}
		return prev(id, def)
	}
	for u := range roles {
		g.tokens[u], _ = login(t, g.srv, u)
	}
	home := map[string]bool{"maya": true, "jack": true, "bob": true, "vic": true, "ada": true, "sam": true}
	g.svc.TenantMemberRole = func(tid, user string) (string, bool) {
		if tid == "home" && home[user] {
			return "member", true
		}
		return "", tid == "other" && (user == "eve" || user == "kim")
	}
	g.svc.RoomMembers = func(_ context.Context, room string) ([]string, error) {
		if room != "room-1" {
			return nil, nil
		}
		return []string{"maya", "jack", "bob"}, nil
	}
	g.svc.OrgAdmins = func(tid string) ([]string, error) {
		if tid == "home" {
			return []string{"ada"}, nil
		}
		return nil, nil
	}
	audit := store.NewMemoryExtensionAudit()
	g.svc.ExtAudit = audit
	outbox := store.NewMemoryNotifications()
	installTestNotifier(t, g.svc.PlatformSettings, "n1")
	g.svc.Alerts = notify.NewEmitter(notify.Stores{Prefs: outbox, Outbox: outbox, Settings: g.svc.PlatformSettings, Installs: store.NewMemoryComponentInstall()}, "")
	return &sharingGateway{contentGateway: g, audit: audit, outbox: outbox}
}

// library lists a member's components of a kind.
func (g *sharingGateway) library(user, kind string) []wire.ContentComponent {
	g.t.Helper()
	code, raw := g.do(user, http.MethodGet, "/v1/content-components?kind="+kind, "", nil)
	if code != http.StatusOK {
		g.t.Fatalf("library of %s = %d %s", user, code, raw)
	}
	var out struct {
		Components []wire.ContentComponent `json:"components"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Components
}

func findContent(list []wire.ContentComponent, name, scope string) *wire.ContentComponent {
	for i := range list {
		if list[i].Name == name && list[i].Scope == scope {
			return &list[i]
		}
	}
	return nil
}

// actions lists the audit rows of kind content as "action name".
func (g *sharingGateway) actions() string {
	recs, _ := g.audit.ExtAuditRecords(context.Background(), wire.AuditKindContent, 0, 0)
	var out []string
	for i := len(recs) - 1; i >= 0; i-- {
		out = append(out, recs[i].Action+" "+recs[i].Name)
	}
	return strings.Join(out, ", ")
}

// queued waits for a notification of typ with dedup key to reach the
// outbox and returns its payload.
func (g *sharingGateway) queued(typ, dedup string) wire.NotificationPayload {
	g.t.Helper()
	id := notify.EventID("home", typ, dedup)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := g.outbox.Item("home", id, "n1"); ok {
			var p wire.NotificationPayload
			_ = json.Unmarshal(it.EventJSON, &p)
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	g.t.Fatalf("no %s notification %s queued", typ, dedup)
	return wire.NotificationPayload{}
}

func (g *sharingGateway) create(user string, form map[string]any) wire.ContentComponent {
	g.t.Helper()
	code, raw := g.json(user, http.MethodPost, "/v1/content-components", form)
	if code != http.StatusCreated {
		g.t.Fatalf("create = %d %s", code, raw)
	}
	return decodeDetail(g.t, raw).Component
}

func (g *sharingGateway) newVersion(user, id, base, instructions string) {
	g.t.Helper()
	form := expertForm(instructions)
	form["baseVersion"] = base
	if code, raw := g.json(user, http.MethodPost, "/v1/content-components/"+id+"/versions", form); code != http.StatusCreated {
		g.t.Fatalf("new version = %d %s", code, raw)
	}
}

// An author shares a version with named members and a room: each gets a
// read-only copy pinned to it, attributed to the author, that the author's
// later versions do not change until the recipient updates it. Only the
// author shares; recipients outside the organization are skipped.
func TestContentShareCopies(t *testing.T) {
	g := newSharingGateway(t)
	form := expertForm("Be strict.")
	form["form"].(map[string]any)["requires"] = map[string]any{"permissions": []string{"scripts"}}
	src := g.create("maya", form)

	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares",
		wire.ContentShareRequest{Members: []string{"jack", "maya", "kim", "nobody", "jack"}, Note: "for your reviews"})
	if code != http.StatusOK {
		t.Fatalf("share = %d %s", code, raw)
	}
	var rep wire.ContentShareReply
	_ = json.Unmarshal(raw, &rep)
	if rep.Version != "1.0.0" || len(rep.Shared) != 1 || rep.Shared[0].Member != "jack" || rep.Shared[0].Status != wire.ContentShareCreated || len(rep.Skipped) != 3 {
		t.Fatalf("share reply = %+v", rep)
	}
	p := g.queued(wire.NotificationContentShared, "share:"+src.ID+"@1.0.0:jack")
	if p.Actor != "maya" || !strings.Contains(p.Title, `maya shared the expert "Reviewer"`) || p.Text != "for your reviews" || !strings.HasSuffix(p.URL, "/#/library") {
		t.Fatalf("notification = %+v", p)
	}

	cp := findContent(g.library("jack", "expert"), "reviewer", "shared")
	if cp == nil || cp.SharedBy != "maya" || cp.SourceID != src.ID || cp.Owner != "jack" || cp.Mine || !cp.Removable || cp.Acked || cp.LatestVersion != "1.0.0" {
		t.Fatalf("jack's copy = %+v", cp)
	}
	if findContent(g.library("bob", "expert"), "reviewer", "shared") != nil {
		t.Fatal("bob sees a copy he was not given")
	}
	// Only the author shares, publishes or edits it.
	if code, _ := g.json("bob", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{Members: []string{"bob"}}); code != http.StatusNotFound {
		t.Fatalf("someone else's share = %d", code)
	}
	if code, raw := g.json("jack", http.MethodPost, "/v1/content-components/"+cp.ID+"/shares", wire.ContentShareRequest{Members: []string{"bob"}}); code != http.StatusConflict {
		t.Fatalf("re-share of a copy = %d %s", code, raw)
	}
	form2 := expertForm("Changed by jack.")
	form2["baseVersion"] = "1.0.0"
	if code, raw := g.json("jack", http.MethodPost, "/v1/content-components/"+cp.ID+"/versions", form2); code != http.StatusConflict || !strings.Contains(string(raw), "read-only") {
		t.Fatalf("new version of a copy = %d %s", code, raw)
	}
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{}); code != http.StatusBadRequest {
		t.Fatalf("empty share = %d", code)
	}

	// The author's new version leaves the copy alone until jack updates it.
	g.newVersion("maya", src.ID, "1.0.0", "Be stricter.")
	cp = findContent(g.library("jack", "expert"), "reviewer", "shared")
	if cp.LatestVersion != "1.0.0" || cp.SourceLatestVersion != "1.0.1" {
		t.Fatalf("copy after the author's edit = %+v", cp)
	}
	code, raw = g.json("jack", http.MethodPost, "/v1/content-components/"+cp.ID+"/update", nil)
	if code != http.StatusOK || decodeDetail(t, raw).Component.LatestVersion != "1.0.1" {
		t.Fatalf("update = %d %s", code, raw)
	}
	if code, _ := g.json("bob", http.MethodPost, "/v1/content-components/"+cp.ID+"/update", nil); code != http.StatusNotFound {
		t.Fatalf("someone else's update = %d", code)
	}
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/update", nil); code != http.StatusConflict {
		t.Fatalf("update of an original = %d", code)
	}

	// A room share reaches its human members (bob new, jack already has
	// 1.0.1); never an older version, never a room the author is not in.
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{RoomID: "room-1", Version: "1.0.0"})
	_ = json.Unmarshal(raw, &rep)
	got := map[string]string{}
	for _, s := range rep.Shared {
		got[s.Member] = s.Status + "@" + s.Version
	}
	if code != http.StatusOK || got["bob"] != "created@1.0.0" || got["jack"] != "unchanged@1.0.1" || len(rep.Shared) != 2 {
		t.Fatalf("room share = %d %+v", code, rep)
	}
	// A later share of the same version notifies its new recipient (only).
	if p := g.queued(wire.NotificationContentShared, "share:"+src.ID+"@1.0.0:bob"); len(p.Recipients) != 1 || p.Recipients[0].UserID != "bob" {
		t.Fatalf("room share notification = %+v", p)
	}
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/shares", wire.ContentShareRequest{RoomID: "room-9"}); code != http.StatusForbidden {
		t.Fatalf("share with a foreign room = %d", code)
	}
	code, raw = g.do("maya", http.MethodGet, "/v1/content-components/"+src.ID+"/shares", "", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"member":"bob"`) || !strings.Contains(string(raw), `"member":"jack"`) {
		t.Fatalf("copies = %d %s", code, raw)
	}

	// The author deletes theirs; the copies stay. jack deletes his copy.
	if code, _ := g.do("maya", http.MethodDelete, "/v1/content-components/"+src.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("author delete = %d", code)
	}
	cp = findContent(g.library("jack", "expert"), "reviewer", "shared")
	if cp == nil || cp.SourceLatestVersion != "" {
		t.Fatalf("copy after the author deleted theirs = %+v", cp)
	}
	if code, _ := g.json("jack", http.MethodPost, "/v1/content-components/"+cp.ID+"/update", nil); code != http.StatusConflict {
		t.Fatalf("update without the author's component = %d", code)
	}
	if code, _ := g.do("jack", http.MethodDelete, "/v1/content-components/"+cp.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("recipient delete = %d", code)
	}
	if a := g.actions(); a != "share reviewer@1.0.0, update reviewer@1.0.1, share reviewer@1.0.0" {
		t.Fatalf("audit = %s", a)
	}
}

// Publishing to the organization is a request an org admin of that
// organization decides after reviewing permissions, scripts and the diff;
// members get the approved version, and a newer one only once approved.
func TestContentOrgApproval(t *testing.T) {
	g := newSharingGateway(t)
	draft := draftExpert("reviewer", map[string]string{
		"component.json":              `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0","displayName":"Reviewer","requires":{"permissions":["scripts"]}}`,
		"skills/lint/SKILL.md":        "---\nname: lint\ndescription: Use to lint.\n---\nRun the script.\n",
		"skills/lint/scripts/lint.sh": "#!/bin/sh\necho lint\n",
	})
	code, raw := g.do("maya", http.MethodPost, "/v1/content-components", "application/x-tar", packageTar(t, draft))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	src := decodeDetail(t, raw).Component

	if code, _ := g.json("jack", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"}); code != http.StatusNotFound {
		t.Fatalf("someone else's publish = %d", code)
	}
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "platform"})
	if code != http.StatusForbidden || !strings.Contains(string(raw), "platform admin") {
		t.Fatalf("platform publish = %d %s", code, raw)
	}
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org", Note: "used it for a month"})
	if code != http.StatusCreated {
		t.Fatalf("publish = %d %s", code, raw)
	}
	var pub wire.ContentPublishReply
	_ = json.Unmarshal(raw, &pub)
	if pub.Request == nil || pub.Request.Status != "pending" || pub.Request.Version != "1.0.0" {
		t.Fatalf("publish reply = %+v", pub)
	}
	reqID := pub.Request.ID
	n := g.queued(wire.NotificationContentReview, "content-request:"+reqID)
	if len(n.Recipients) != 1 || n.Recipients[0].UserID != "ada" || !strings.HasSuffix(n.URL, "/#/org") {
		t.Fatalf("request notification = %+v", n)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "org") != nil {
		t.Fatal("a pending request is visible to members")
	}

	// Only org admins of that organization review and decide.
	for _, u := range []string{"jack", "eve"} {
		if code, _ := g.do(u, http.MethodGet, "/v1/tenant/content-requests/"+reqID, "", nil); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Fatalf("%s review = %d", u, code)
		}
		if code, _ := g.json(u, http.MethodPost, "/v1/tenant/content-requests/"+reqID+"/approve", nil); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Fatalf("%s approve = %d", u, code)
		}
	}
	code, raw = g.do("ada", http.MethodGet, "/v1/tenant/content-requests", "", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), reqID) || !strings.Contains(string(raw), `"displayName":"Reviewer"`) {
		t.Fatalf("pending = %d %s", code, raw)
	}
	var rv wire.ContentReview
	code, raw = g.do("ada", http.MethodGet, "/v1/tenant/content-requests/"+reqID, "", nil)
	_ = json.Unmarshal(raw, &rv)
	if code != http.StatusOK || rv.Previous != nil || strings.Join(rv.Component.Permissions, ",") != "scripts" || strings.Join(rv.ScriptFiles, ",") != "skills/lint/scripts/lint.sh" ||
		len(rv.Files) != 4 || rv.Files[0].Change != wire.ContentFileAdded || rv.Request.Note != "used it for a month" {
		t.Fatalf("review = %d %+v", code, rv)
	}

	code, raw = g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+reqID+"/approve", wire.ContentDecisionInput{Note: "looks safe"})
	if code != http.StatusOK || !strings.Contains(string(raw), `"status":"approved"`) {
		t.Fatalf("approve = %d %s", code, raw)
	}
	if code, _ := g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+reqID+"/decline", nil); code != http.StatusConflict {
		t.Fatalf("second decision = %d", code)
	}
	d := g.queued(wire.NotificationContentReview, "content-decision:"+reqID)
	if d.Recipients[0].UserID != "maya" || !strings.Contains(d.Title, "is published to your organization") || d.Text != "looks safe" {
		t.Fatalf("decision notification = %+v", d)
	}
	org := findContent(g.library("jack", "expert"), "reviewer", "org")
	if org == nil || org.Owner != "maya" || org.LatestVersion != "1.0.0" || org.Mine || org.Removable || org.Acked {
		t.Fatalf("jack's view of the org expert = %+v", org)
	}
	if g.library("kim", "expert") != nil && findContent(g.library("kim", "expert"), "reviewer", "org") != nil {
		t.Fatal("another organization sees it")
	}
	mine := g.library("maya", "expert")
	if findContent(mine, "reviewer", "org") != nil {
		t.Fatal("the author sees her publication as a second card")
	}
	if p := findContent(mine, "reviewer", "personal").Publication; p == nil || p.OrgVersion != "1.0.0" || p.OrgStatus != "active" || p.Request.Status != "approved" {
		t.Fatalf("author's publication = %+v", p)
	}

	// A newer version needs approval again; members stay on 1.0.0. A
	// decline leaves the organization's version.
	code, raw = g.do("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", "application/x-tar", packageTar(t, map[string]string{
		"component.json":              `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.1.0","displayName":"Reviewer","requires":{"permissions":["scripts"]}}`,
		"INSTRUCTIONS.md":             "Be thorough and fast.\n",
		"skills/lint/SKILL.md":        "---\nname: lint\ndescription: Use to lint.\n---\nRun the script.\n",
		"skills/lint/scripts/lint.sh": "#!/bin/sh\necho lint\n",
	}))
	if code != http.StatusCreated {
		t.Fatalf("new version = %d %s", code, raw)
	}
	// The organization uses it, so the new version asked for approval by
	// itself; asking again supersedes that request.
	if rs := decodeDetail(t, raw).Resubmitted; rs == nil || rs.Version != "1.0.1" || rs.ApprovedVersion != "1.0.0" {
		t.Fatalf("resubmitted = %+v", rs)
	}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org", Version: "1.0.0"}); code != http.StatusConflict {
		t.Fatalf("republish the approved version = %d %s", code, raw)
	}
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"})
	_ = json.Unmarshal(raw, &pub)
	if code != http.StatusCreated || pub.Request.ApprovedVersion != "1.0.0" || !strings.Contains(pub.Note, "keep using version 1.0.0") {
		t.Fatalf("second publish = %d %+v", code, pub)
	}
	if v := findContent(g.library("jack", "expert"), "reviewer", "org"); v.LatestVersion != "1.0.0" {
		t.Fatalf("members moved before approval: %+v", v)
	}
	code, raw = g.do("ada", http.MethodGet, "/v1/tenant/content-requests/"+pub.Request.ID, "", nil)
	rv = wire.ContentReview{}
	_ = json.Unmarshal(raw, &rv)
	if code != http.StatusOK || rv.Previous == nil || rv.Previous.Version != "1.0.0" || len(rv.Files) != 2 {
		t.Fatalf("second review = %d %s", code, raw)
	}
	for _, f := range rv.Files {
		if f.Path == "INSTRUCTIONS.md" && (f.Change != wire.ContentFileChanged || f.Before != "Be thorough.\n" || f.After != "Be thorough and fast.\n") {
			t.Fatalf("instructions diff = %+v", f)
		}
	}
	if code, _ := g.json("ada", http.MethodPost, "/v1/tenant/content-requests/"+pub.Request.ID+"/decline", wire.ContentDecisionInput{Note: "too fast"}); code != http.StatusOK {
		t.Fatalf("decline = %d", code)
	}
	if v := findContent(g.library("jack", "expert"), "reviewer", "org"); v.LatestVersion != "1.0.0" {
		t.Fatalf("a decline moved members: %+v", v)
	}
	if p := findContent(g.library("maya", "expert"), "reviewer", "personal").Publication; p.Request.Status != "declined" || p.Request.DecisionNote != "too fast" || p.OrgVersion != "1.0.0" {
		t.Fatalf("declined publication = %+v", p.Request)
	}

	// Admins unpublish, restore and remove; the author takes hers out too.
	orgID := org.ID
	if code, _ := g.json("jack", http.MethodPost, "/v1/tenant/content-components/"+orgID+"/unpublish", nil); code != http.StatusForbidden {
		t.Fatalf("member unpublish = %d", code)
	}
	if code, _ := g.json("eve", http.MethodPost, "/v1/tenant/content-components/"+orgID+"/unpublish", nil); code != http.StatusNotFound {
		t.Fatalf("foreign admin unpublish = %d", code)
	}
	if code, _ := g.json("ada", http.MethodPost, "/v1/tenant/content-components/"+orgID+"/unpublish", nil); code != http.StatusNoContent {
		t.Fatalf("unpublish = %d", code)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "org") != nil {
		t.Fatal("unpublished content is listed")
	}
	code, raw = g.do("ada", http.MethodGet, "/v1/tenant/content-components", "", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"status":"retired"`) {
		t.Fatalf("admin listing = %d %s", code, raw)
	}
	if code, _ := g.json("ada", http.MethodPost, "/v1/tenant/content-components/"+orgID+"/restore", nil); code != http.StatusNoContent {
		t.Fatalf("restore = %d", code)
	}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "personal"}); code != http.StatusOK {
		t.Fatalf("author withdraws = %d %s", code, raw)
	}
	if findContent(g.library("jack", "expert"), "reviewer", "org") != nil {
		t.Fatal("withdrawn content is listed")
	}
	if code, _ := g.do("ada", http.MethodDelete, "/v1/tenant/content-components/"+orgID, "", nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if a := g.actions(); a != "request reviewer@1.0.0, approve reviewer@1.0.0, request reviewer@1.0.1, request reviewer@1.0.1, decline reviewer@1.0.1, unpublish reviewer@1.0.0, restore reviewer@1.0.0, unpublish reviewer@1.0.0, remove reviewer@1.0.0" {
		t.Fatalf("audit = %s", a)
	}
}

// A super admin exports a version as a package, signs it offline and
// installs it through the component route: every organization's members
// get it. Members and org admins cannot; a tampered or unsigned package is
// refused.
func TestContentPlatformPromote(t *testing.T) {
	g := newSharingGateway(t)
	src := g.create("maya", expertForm("Be strict."))
	if code, _ := g.do("ada", http.MethodGet, "/v1/platform/content-components/"+src.ID+"/package", "", nil); code != http.StatusForbidden {
		t.Fatalf("org admin export = %d", code)
	}
	code, body := g.do("sam", http.MethodGet, "/v1/platform/content-components/"+src.ID+"/package?publisher=acme", "", nil)
	if code != http.StatusOK {
		t.Fatalf("export = %d %s", code, body)
	}
	pkg, err := pkgmeta.ParsePackage(body)
	if err != nil || pkg.Kind != "bnbloc.expert.package/v1" || pkg.Name != "reviewer" || pkg.Version != "1.0.0" || pkg.Publisher != "acme" {
		t.Fatalf("package = %+v, %v", pkg, err)
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	trusted, _ := json.Marshal([]pkgmeta.TrustedKey{{KeyID: "0102030405060708", PublicKey: minisignTestPublicKey("0102030405060708", pub)}})
	_ = g.svc.PlatformSettings.PutPlatformSetting(context.Background(), store.PlatformSetting{Key: componentTrustedKeysKey, Value: string(trusted)})
	install := func(user string, body []byte, sig, rollout string) (int, []byte) {
		return g.json(user, http.MethodPost, "/v1/platform/components", componentInstallRequest{Package: string(body), Signature: sig, Rollout: rollout})
	}
	if code, raw := install("sam", body, "", ""); code != http.StatusBadRequest || !strings.Contains(string(raw), "signature required") {
		t.Fatalf("unsigned = %d %s", code, raw)
	}
	if code, _ := install("sam", body, signPackage(priv, body), "optin"); code != http.StatusBadRequest {
		t.Fatalf("optin = %d", code)
	}
	if code, _ := install("ada", body, signPackage(priv, body), ""); code != http.StatusForbidden {
		t.Fatalf("org admin install = %d", code)
	}
	code, raw := install("sam", body, signPackage(priv, body), "")
	if code != http.StatusOK || !strings.Contains(string(raw), `"scope":"platform"`) {
		t.Fatalf("install = %d %s", code, raw)
	}
	var inst contentInstallReply
	_ = json.Unmarshal(raw, &inst)
	if v, err := g.svc.ContentComponents.GetContentComponentVersion(context.Background(), inst.ID, "1.0.0"); err != nil || v.Signature == "" {
		t.Fatalf("signed version = %+v, %v", v, err)
	}
	for _, u := range []string{"jack", "kim"} {
		c := findContent(g.library(u, "expert"), "reviewer", "platform")
		if c == nil || c.Owner != "" || c.Mine || c.Removable {
			t.Fatalf("%s's platform expert = %+v", u, c)
		}
	}
	// Again: idempotent. The same version with other content: refused.
	if code, raw := install("sam", body, signPackage(priv, body), ""); code != http.StatusOK {
		t.Fatalf("reinstall = %d %s", code, raw)
	}
	other := g.create("bob", expertForm("Something else."))
	_, otherBody := g.do("sam", http.MethodGet, "/v1/platform/content-components/"+other.ID+"/package", "", nil)
	if code, raw := install("sam", otherBody, signPackage(priv, otherBody), ""); code != http.StatusConflict {
		t.Fatalf("same version, other content = %d %s", code, raw)
	}
	tampered := []byte(strings.Replace(string(body), `"acme"`, `"evil"`, 1))
	if code, _ := install("sam", tampered, signPackage(priv, body), ""); code != http.StatusBadRequest {
		t.Fatalf("tampered = %d", code)
	}
	// Unpublish hides it everywhere.
	if code, _ := g.json("sam", http.MethodPost, "/v1/platform/content-components/"+inst.ID+"/unpublish", nil); code != http.StatusNoContent {
		t.Fatalf("unpublish = %d", code)
	}
	if findContent(g.library("kim", "expert"), "reviewer", "platform") != nil {
		t.Fatal("unpublished platform content is listed")
	}
	code, raw = g.do("sam", http.MethodGet, "/v1/platform/content-components?scope=platform", "", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), inst.ID) {
		t.Fatalf("platform listing = %d %s", code, raw)
	}
	if a := g.actions(); a != "install reviewer@1.0.0, install reviewer@1.0.0, unpublish reviewer@1.0.0" {
		t.Fatalf("audit = %s", a)
	}
}

// publish_component (room tools) publishes as the job's requester through
// the job grant: the request is the requester's, and the audit names the
// agent that asked.
func TestContentPublishAsJobRequester(t *testing.T) {
	g := newSharingGateway(t)
	src := g.create("maya", expertForm("Be strict."))
	ctx := tenant.NewContext(context.Background(), "home")
	now := time.Now().UnixMilli()
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	code, raw := g.json("jack", http.MethodPost, "/v1/content-components/"+src.ID+"/publish?job=job-1&room=room-1", wire.ContentPublishInput{Scope: "org"})
	if code != http.StatusCreated || !strings.Contains(string(raw), `"requestedBy":"maya"`) {
		t.Fatalf("granted publish = %d %s", code, raw)
	}
	if code, _ := g.json("jack", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"}); code != http.StatusNotFound {
		t.Fatalf("publish without the grant = %d", code)
	}
	recs, _ := g.audit.ExtAuditRecords(context.Background(), wire.AuditKindContent, 0, 0)
	if len(recs) != 1 || recs[0].Actor != "maya" || !strings.Contains(recs[0].Detail, "through agent jack/opencode, job job-1") {
		t.Fatalf("audit = %+v", recs)
	}
}
