package room

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// sharesRoom is filesRoom with a tenant registry (alice and bob members,
// olga an org admin who is not in the room) and a controllable clock.
func sharesRoom(t *testing.T) (*handoffEnv, *store.MemoryTenant, *int64) {
	t.Helper()
	e := filesRoom(t)
	tenants := store.NewMemoryTenant()
	ctx := testCtx(t)
	if err := tenants.PutTenant(ctx, store.Tenant{TenantID: tenant.Default, Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	for user, role := range map[string]store.OrgRole{"alice": store.OrgRoleMember, "bob": store.OrgRoleMember, "carol": store.OrgRoleMember, "olga": store.OrgRoleAdmin} {
		if err := tenants.PutMember(ctx, store.TenantMember{TenantID: tenant.Default, UserID: user, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.Tenants = tenants
	e.svc.Profiles = store.NewMemoryUserProfiles()
	now := int64(1_700_000_000_000)
	e.svc.Now = func() int64 { return now }
	return e, tenants, &now
}

type shareReply struct {
	Share  wire.ShareView   `json:"share"`
	Token  string           `json:"token"`
	Shares []wire.ShareView `json:"shares"`
	Error  string           `json:"error"`
}

func (e *handoffEnv) share(method, path, user string, body any) (int, shareReply) {
	e.t.Helper()
	code, raw := e.do(e.roomSrv, method, path, user, body)
	var out shareReply
	_ = json.Unmarshal(raw, &out)
	return code, out
}

func sharesPath(artifactID string) string {
	return "/v1/rooms/files/artifacts/" + artifactID + "/shares"
}

// resolve calls the internal resolve route as the gateway.
func (e *handoffEnv) resolve(token string, countView bool) (int, wire.ShareResolution) {
	e.t.Helper()
	code, raw := e.do(e.roomSrv, http.MethodPost, "/internal/v1/shares/resolve", svcauth.ServiceSubject("gateway"),
		map[string]any{"token": token, "countView": countView})
	var out wire.ShareResolution
	_ = json.Unmarshal(raw, &out)
	return code, out
}

func (e *handoffEnv) publishText(user, artifactID string, base int, content string) {
	e.t.Helper()
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts/"+artifactID+"/versions", user,
		map[string]any{"baseVersion": base, "content": content})
}

// A member creates and lists links; the body is validated, the token is
// shown once in the documented shape, and non-members are refused.
func TestShareCreateAndList(t *testing.T) {
	e, _, now := sharesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "fileName": "spec.md", "content": "# v1"})

	code, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, rep.Error)
	}
	sh := rep.Share
	if !strings.HasPrefix(rep.Token, "bsh_"+sh.ShareID+"_") || len(sh.ShareID) != 32 {
		t.Fatalf("token %q for share %q", rep.Token, sh.ShareID)
	}
	if sh.Version != 0 || sh.CreatedBy != "bob" || sh.ArtifactID != art.ArtifactID || !sh.Active ||
		sh.CreatedAtMs != *now || sh.ExpiresAtMs != *now+168*hourMs || sh.ViewCount != 0 {
		t.Fatalf("share = %+v", sh)
	}
	stored, err := e.svc.Artifacts.GetArtifactShare(testCtx(t), sh.ShareID)
	if err != nil || strings.Contains(rep.Token, stored.TokenHash) || len(stored.TokenHash) != 64 {
		t.Fatalf("stored = %+v %v (only the secret's hash is stored)", stored, err)
	}

	*now += 10
	code, pinned := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", map[string]any{"version": 1, "expiresInHours": 1})
	if code != http.StatusCreated || pinned.Share.Version != 1 || pinned.Share.ExpiresAtMs != *now+hourMs {
		t.Fatalf("pinned = %d %+v", code, pinned)
	}
	for _, bad := range []map[string]any{
		{"expiresInHours": 2161}, {"expiresInHours": -1}, {"version": -1}, {"version": 2},
	} {
		if code, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", bad); code != http.StatusBadRequest {
			t.Fatalf("create %v = %d %s, want 400", bad, code, rep.Error)
		}
	}
	if code, _ := e.share(http.MethodPost, sharesPath(art.ArtifactID), "carol", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("non-member create = %d, want 403", code)
	}
	if code, _ := e.share(http.MethodPost, sharesPath("art_missing"), "alice", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("unknown artifact = %d, want 404", code)
	}

	code, list := e.share(http.MethodGet, sharesPath(art.ArtifactID), "bob", nil)
	if code != http.StatusOK || len(list.Shares) != 2 || list.Shares[0].ShareID != pinned.Share.ShareID || list.Shares[1].ShareID != sh.ShareID {
		t.Fatalf("list = %d %+v (newest first)", code, list)
	}
	if code, _ := e.share(http.MethodGet, sharesPath(art.ArtifactID), "carol", nil); code != http.StatusForbidden {
		t.Fatalf("non-member list = %d, want 403", code)
	}
	// An expired link stays listed (inactive) for 30 days, then drops.
	*now += 2 * hourMs
	_, list = e.share(http.MethodGet, sharesPath(art.ArtifactID), "bob", nil)
	if len(list.Shares) != 2 || list.Shares[0].Active || !list.Shares[1].Active {
		t.Fatalf("after the pinned link expired: %+v", list.Shares)
	}
	*now += 40 * 24 * hourMs
	_, list = e.share(http.MethodGet, sharesPath(art.ArtifactID), "bob", nil)
	if len(list.Shares) != 0 {
		t.Fatalf("links ended over 30 days ago are listed: %+v", list.Shares)
	}
}

// Resolve answers an active link with its artifact and version — the latest
// one at call time for a follow-latest link — counts views on request, and
// answers the same 404 for every inactive case.
func TestShareResolve(t *testing.T) {
	e, tenants, now := sharesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "fileName": "spec.md", "content": "# v1"})
	_, latest := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	_, pinned := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", map[string]any{"version": 1, "expiresInHours": 1})

	code, res := e.resolve(latest.Token, true)
	if code != http.StatusOK || res.TenantID != tenant.Default || res.RoomID != "files" || res.Share.ShareID != latest.Share.ShareID ||
		res.Artifact.ArtifactID != art.ArtifactID || res.Artifact.Kind != "markdown" || res.Artifact.Title != "Spec" || res.Artifact.FileName != "spec.md" ||
		res.Version.Version != 1 || res.Version.Storage != store.ArtifactStorageInline || res.Version.Content != "# v1" || res.Version.MimeType != "text/markdown" ||
		res.Version.BlobKey != "" || res.Version.ContentHash != store.HashArtifactContent("# v1") ||
		res.SharedBy.UserID != "bob" || res.SharedBy.DisplayName != "bob" || res.Share.ViewCount != 1 || res.Share.LastViewedAtMs != *now {
		t.Fatalf("resolve = %d %+v", code, res)
	}
	// A new version: the latest link follows it, the pinned one does not.
	e.publishText("alice", art.ArtifactID, 1, "# v2")
	if _, res := e.resolve(latest.Token, false); res.Version.Version != 2 || res.Version.Content != "# v2" || res.Share.ViewCount != 1 {
		t.Fatalf("latest after v2 = %+v (countView=false must not count)", res)
	}
	if _, res := e.resolve(pinned.Token, true); res.Version.Version != 1 || res.Share.ViewCount != 1 {
		t.Fatalf("pinned after v2 = %+v", res)
	}
	if sh, _ := e.svc.Artifacts.GetArtifactShare(testCtx(t), latest.Share.ShareID); sh.ViewCount != 1 {
		t.Fatalf("stored views = %d, want 1", sh.ViewCount)
	}

	// Only the gateway's service subject may resolve.
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/internal/v1/shares/resolve", "alice", map[string]any{"token": latest.Token}); code != http.StatusForbidden {
		t.Fatalf("member resolve = %d, want 403", code)
	}
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/internal/v1/shares/resolve", "", map[string]any{"token": latest.Token}); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("anonymous resolve = %d, want refused", code)
	}

	id := latest.Share.ShareID
	secret := strings.TrimPrefix(latest.Token, "bsh_"+id+"_")
	flipped := []byte(secret)
	flipped[0] ^= 1
	for name, tok := range map[string]string{
		"malformed":  "not-a-token",
		"unknown id": "bsh_" + strings.Repeat("0", 32) + "_" + secret,
		"bad secret": "bsh_" + id + "_" + string(flipped),
		"no secret":  "bsh_" + id + "_",
	} {
		if code, _ := e.resolve(tok, true); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", name, code)
		}
	}

	// Expired.
	*now += hourMs
	if code, _ := e.resolve(pinned.Token, false); code != http.StatusNotFound {
		t.Fatalf("expired = %d, want 404", code)
	}
	// Org-disabled: resolve 404, create 403 with the contract text; re-enabled works again.
	on, off := true, false
	if _, err := tenants.SetTenantPolicy(testCtx(t), tenant.Default, store.TenantPolicyUpdate{PublicSharesDisabled: &on}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.resolve(latest.Token, false); code != http.StatusNotFound {
		t.Fatalf("disabled = %d, want 404", code)
	}
	if code, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", map[string]any{}); code != http.StatusForbidden || rep.Error != "public share links are disabled for this organization" {
		t.Fatalf("create while disabled = %d %q", code, rep.Error)
	}
	if _, err := tenants.SetTenantPolicy(testCtx(t), tenant.Default, store.TenantPolicyUpdate{PublicSharesDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.resolve(latest.Token, false); code != http.StatusOK {
		t.Fatalf("re-enabled = %d, want 200", code)
	}
	// Revoked.
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions", "alice", map[string]any{"baseVersion": 2, "content": "# v3"})
	if code, _ := e.do(e.roomSrv, http.MethodDelete, sharesPath(art.ArtifactID)+"/"+id, "bob", nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if code, _ := e.resolve(latest.Token, false); code != http.StatusNotFound {
		t.Fatalf("revoked = %d, want 404", code)
	}
	// Room gone.
	_, fresh := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", map[string]any{})
	if code, _ := e.resolve(fresh.Token, false); code != http.StatusOK {
		t.Fatalf("fresh = %d", code)
	}
	e.must(e.roomSrv, http.MethodDelete, "/v1/rooms/files", "alice", nil)
	if code, _ := e.resolve(fresh.Token, false); code != http.StatusNotFound {
		t.Fatalf("room deleted = %d, want 404", code)
	}
}

// A blob version resolves to its blob key, never inline bytes; an artifact
// with no version yet resolves 404.
func TestShareResolveBlobAndEmpty(t *testing.T) {
	e, _, _ := sharesRoom(t)
	empty := e.createArtifact("alice", map[string]any{"kind": "file", "title": "later.pdf", "fileName": "later.pdf"})
	_, rep := e.share(http.MethodPost, sharesPath(empty.ArtifactID), "alice", map[string]any{})
	if code, _ := e.resolve(rep.Token, false); code != http.StatusNotFound {
		t.Fatalf("no version yet = %d, want 404", code)
	}
	pdf := []byte("%PDF-1.4\n%binary\x00\x01\n")
	if code, v := e.postFile("alice", empty.ArtifactID, map[string]string{"baseVersion": "0", "summary": "draft"}, "later.pdf", "", pdf); code != http.StatusOK {
		t.Fatalf("publish = %d %s", code, v.Error)
	}
	code, res := e.resolve(rep.Token, false)
	if code != http.StatusOK || res.Version.Storage != store.ArtifactStorageBlob || !strings.HasPrefix(res.Version.BlobKey, "sha256/") ||
		res.Version.Content != "" || res.Version.MimeType != "application/pdf" || res.Version.SizeBytes != int64(len(pdf)) || res.Version.Summary != "draft" {
		t.Fatalf("blob resolve = %d %+v", code, res)
	}
}

// Revoking is open to the link's creator, the room's owner/admins, and an
// org admin (even one outside the room); nobody else.
func TestShareRevokeAuthorization(t *testing.T) {
	e, _, _ := sharesRoom(t)
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/members", "alice", map[string]any{"userId": "dave", "role": "member"})
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "content": "x"})
	other := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Other", "content": "y"})
	mk := func(user string) string {
		_, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), user, map[string]any{})
		return rep.Share.ShareID
	}
	// The gateway signs each caller's org role in the room's tenant; only
	// olga is an org admin.
	revoke := func(user, artifactID, shareID string) int {
		req, _ := http.NewRequest(http.MethodDelete, e.roomSrv.URL+sharesPath(artifactID)+"/"+shareID, nil)
		role := string(rbac.RoleMember)
		if user == "olga" {
			role = string(rbac.RoleAdmin)
		}
		svcauth.Key("").Assert(req, user, tenant.Default, role)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	bobs := mk("bob")
	if code := revoke("dave", art.ArtifactID, bobs); code != http.StatusForbidden {
		t.Fatalf("another member = %d, want 403", code)
	}
	if code := revoke("carol", art.ArtifactID, bobs); code != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", code)
	}
	if code := revoke("bob", other.ArtifactID, bobs); code != http.StatusNotFound {
		t.Fatalf("wrong artifact path = %d, want 404", code)
	}
	if code := revoke("bob", art.ArtifactID, strings.Repeat("f", 32)); code != http.StatusNotFound {
		t.Fatalf("unknown share = %d, want 404", code)
	}
	if code := revoke("bob", art.ArtifactID, bobs); code != http.StatusNoContent {
		t.Fatalf("creator = %d, want 204", code)
	}
	if code := revoke("bob", art.ArtifactID, bobs); code != http.StatusNoContent {
		t.Fatalf("repeat revoke = %d, want 204 (idempotent)", code)
	}
	if code := revoke("alice", art.ArtifactID, mk("bob")); code != http.StatusNoContent {
		t.Fatalf("room owner = %d, want 204", code)
	}
	dave := mk("dave")
	if code := revoke("olga", art.ArtifactID, dave); code != http.StatusNoContent {
		t.Fatalf("org admin outside the room = %d, want 204", code)
	}
	sh, _ := e.svc.Artifacts.GetArtifactShare(testCtx(t), dave)
	if sh.RevokedBy != "olga" || sh.RevokedAtMs == 0 {
		t.Fatalf("revocation not recorded: %+v", sh)
	}
	// The org-admin pass is for revoking only.
	if code, _ := e.share(http.MethodGet, sharesPath(art.ArtifactID), "olga", nil); code != http.StatusForbidden {
		t.Fatalf("org admin list of a foreign room = %d, want 403", code)
	}
}

// The org-admin list spans the tenant's rooms with titles; ?active=1 keeps
// only links that work now.
func TestTenantShareList(t *testing.T) {
	e, _, _ := sharesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "content": "x"})
	_, a := e.share(http.MethodPost, sharesPath(art.ArtifactID), "alice", map[string]any{})
	_, b := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	e.do(e.roomSrv, http.MethodDelete, sharesPath(art.ArtifactID)+"/"+b.Share.ShareID, "bob", nil)

	get := func(q string) []tenantShareView {
		code, raw := e.do(e.roomSrv, http.MethodGet, "/v1/tenant/shares"+q, "olga", nil)
		if code != http.StatusOK {
			t.Fatalf("tenant shares%s = %d %s", q, code, raw)
		}
		var out struct {
			Shares []tenantShareView `json:"shares"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out.Shares
	}
	all := get("")
	if len(all) != 2 {
		t.Fatalf("all = %+v", all)
	}
	active := get("?active=1")
	if len(active) != 1 || active[0].ShareID != a.Share.ShareID || active[0].RoomID != "files" || active[0].ArtifactTitle != "Spec" || !active[0].Active {
		t.Fatalf("active = %+v", active)
	}
	// The wire shape flattens ShareView with roomId and artifactTitle.
	_, raw := e.do(e.roomSrv, http.MethodGet, "/v1/tenant/shares?active=1", "olga", nil)
	for _, key := range []string{`"shareId"`, `"roomId":"files"`, `"artifactTitle":"Spec"`, `"active":true`, `"viewCount":0`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("tenant list %s lacks %s", raw, key)
		}
	}
}

// A site is a blob of tarbundle.SiteMIME: inline content is refused, other
// types are refused, the type is refused on other kinds, and the content
// route serves the tar as a sandboxed attachment, never as HTML.
func TestSiteArtifactVersions(t *testing.T) {
	e := filesRoom(t)
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts", "alice", map[string]any{"kind": "site", "title": "App", "content": "<html>"}); code != http.StatusBadRequest {
		t.Fatalf("inline site create = %d, want 400", code)
	}
	site := e.createArtifact("alice", map[string]any{"kind": "site", "title": "App", "fileName": "dist"})
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts/"+site.ArtifactID+"/versions", "alice", map[string]any{"baseVersion": 0, "content": "<html>"}); code != http.StatusBadRequest {
		t.Fatalf("inline site version = %d, want 400", code)
	}
	tarball, err := tarbundle.PackFiles(map[string][]byte{"index.html": []byte("<!doctype html><script>alert(1)</script>")}, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.postFile("alice", site.ArtifactID, map[string]string{"baseVersion": "0"}, "dist", "text/html", tarball); code != http.StatusUnsupportedMediaType {
		t.Fatalf("site as text/html = %d, want 415", code)
	}
	code, rep := e.postFile("alice", site.ArtifactID, map[string]string{"baseVersion": "0"}, "dist", tarbundle.SiteMIME, tarball)
	if code != http.StatusOK || rep.Version.MimeType != tarbundle.SiteMIME || rep.Version.Storage != store.ArtifactStorageBlob {
		t.Fatalf("site publish = %d %+v", code, rep)
	}
	// Undeclared type: a site version is typed as a site snapshot.
	code, rep = e.postFile("alice", site.ArtifactID, map[string]string{"baseVersion": "1"}, "dist", "", append(tarball, make([]byte, 1024)...))
	if code != http.StatusOK || rep.Version.MimeType != tarbundle.SiteMIME {
		t.Fatalf("undeclared site publish = %d %+v", code, rep)
	}
	resp := e.get("/v1/rooms/files/artifacts/"+site.ArtifactID+"/versions/1/content", "bob")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != http.StatusOK || string(body) != string(tarball) || h.Get("Content-Type") != tarbundle.SiteMIME ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("site content = %d %v", resp.StatusCode, h)
	}
	file := e.createArtifact("alice", map[string]any{"kind": "file", "title": "bundle.tar", "fileName": "bundle.tar"})
	if code, _ := e.postFile("alice", file.ArtifactID, map[string]string{"baseVersion": "0"}, "bundle.tar", tarbundle.SiteMIME, tarball); code != http.StatusUnsupportedMediaType {
		t.Fatalf("site type on a file = %d, want 415", code)
	}
	if code, out := e.do(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts", "alice", map[string]any{"kind": "web", "title": "x"}); code != http.StatusBadRequest || !strings.Contains(string(out), "site") {
		t.Fatalf("bad kind = %d %s", code, out)
	}
}
