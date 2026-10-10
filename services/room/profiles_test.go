package room

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func newProfileServer(t *testing.T) (string, *Service) {
	t.Helper()
	srv, svc := newTestServer(t)
	svc.Profiles = store.NewMemoryUserProfiles()
	return srv.URL, svc
}

func decodeInto[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func closeStatus(t *testing.T, resp *http.Response) int {
	t.Helper()
	resp.Body.Close()
	return resp.StatusCode
}

// The caller's own profile: the default until a name is set, the trimmed
// name after, and the default again once cleared.
func TestMyProfileSetReadClear(t *testing.T) {
	base, _ := newProfileServer(t)
	const me = "jane.d@example.com"

	resp := do(t, http.MethodGet, base+"/v1/my/profile", me, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d", closeStatus(t, resp))
	}
	if p := decodeInto[wire.MyProfile](t, resp); p != (wire.MyProfile{UserID: me, DisplayName: "jane.d", IsDefault: true}) {
		t.Fatalf("default profile = %+v", p)
	}

	resp = do(t, http.MethodPut, base+"/v1/my/profile", me, map[string]string{"displayName": "  Maya  "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d", closeStatus(t, resp))
	}
	want := wire.MyProfile{UserID: me, DisplayName: "Maya"}
	if p := decodeInto[wire.MyProfile](t, resp); p != want {
		t.Fatalf("put answer = %+v, want %+v", p, want)
	}
	if p := decodeInto[wire.MyProfile](t, do(t, http.MethodGet, base+"/v1/my/profile", me, nil)); p != want {
		t.Fatalf("read back = %+v, want %+v", p, want)
	}

	resp = do(t, http.MethodPut, base+"/v1/my/profile", me, map[string]string{"displayName": "   "})
	if p := decodeInto[wire.MyProfile](t, resp); p != (wire.MyProfile{UserID: me, DisplayName: "jane.d", IsDefault: true}) {
		t.Fatalf("cleared = %+v", p)
	}
}

// Invalid names are refused and leave the stored name unchanged; the routes
// need an authenticated caller.
func TestMyProfileValidationAndAuth(t *testing.T) {
	base, _ := newProfileServer(t)
	if code := closeStatus(t, do(t, http.MethodPut, base+"/v1/my/profile", "alice", map[string]string{"displayName": "Alice"})); code != http.StatusOK {
		t.Fatalf("seed = %d", code)
	}
	for _, bad := range []string{strings.Repeat("a", 41), "two\nlines", "tab\tinside", "nul\x00"} {
		resp := do(t, http.MethodPut, base+"/v1/my/profile", "alice", map[string]string{"displayName": bad})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT %q = %d, want 400", bad, closeStatus(t, resp))
		}
		if e := decodeInto[map[string]string](t, resp); !strings.Contains(e["error"], "display name") {
			t.Fatalf("PUT %q error = %q", bad, e["error"])
		}
	}
	if p := decodeInto[wire.MyProfile](t, do(t, http.MethodGet, base+"/v1/my/profile", "alice", nil)); p.DisplayName != "Alice" {
		t.Fatalf("after refused writes = %+v", p)
	}
	if code := closeStatus(t, do(t, http.MethodPut, base+"/v1/my/profile", "alice", map[string]string{"displayName": strings.Repeat("名", 40)})); code != http.StatusOK {
		t.Fatalf("40 characters = %d, want 200", code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/my/profile"},
		{http.MethodPut, "/v1/my/profile"},
		{http.MethodGet, "/v1/users/profiles?ids=alice"},
	} {
		if code := closeStatus(t, do(t, c.method, base+c.path, "", map[string]string{"displayName": "x"})); code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d, want 401", c.method, c.path, code)
		}
	}
}

// The batch lookup answers any signed-in caller for any ids — set or not,
// known or not — with only the display name, each distinct id once.
func TestUserProfilesBatch(t *testing.T) {
	base, svc := newProfileServer(t)
	if err := svc.Profiles.SetDisplayName(dctx(), "alice@x.io", "Alice", 1); err != nil {
		t.Fatal(err)
	}
	q := url.QueryEscape("alice@x.io, bob@y.org,carol,,ALICE@x.io") + "&ids=" + url.QueryEscape("dave@z")
	resp := do(t, http.MethodGet, base+"/v1/users/profiles?ids="+q, "stranger@else.where", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d", closeStatus(t, resp))
	}
	got := decodeInto[struct {
		Profiles []wire.UserProfile `json:"profiles"`
	}](t, resp).Profiles
	want := []wire.UserProfile{
		{UserID: "alice@x.io", DisplayName: "Alice"},
		{UserID: "bob@y.org", DisplayName: "bob"},
		{UserID: "carol", DisplayName: "carol"},
		{UserID: "dave@z", DisplayName: "dave"},
	}
	if len(got) != len(want) {
		t.Fatalf("profiles = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("profiles[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if code := closeStatus(t, do(t, http.MethodGet, base+"/v1/users/profiles", "alice@x.io", nil)); code != http.StatusBadRequest {
		t.Fatalf("no ids = %d, want 400", code)
	}
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = "u" + strconv.Itoa(i)
	}
	if code := closeStatus(t, do(t, http.MethodGet, base+"/v1/users/profiles?ids="+strings.Join(ids, ","), "alice@x.io", nil)); code != http.StatusOK {
		t.Fatalf("200 ids = %d, want 200", code)
	}
	if code := closeStatus(t, do(t, http.MethodGet, base+"/v1/users/profiles?ids="+strings.Join(append(ids, "u200"), ","), "alice@x.io", nil)); code != http.StatusBadRequest {
		t.Fatalf("201 ids = %d, want 400", code)
	}
	// /v1/users/{id}/rooms is unaffected by the static profiles route.
	if code := closeStatus(t, do(t, http.MethodGet, base+"/v1/users/profiles/rooms", "profiles", nil)); code != http.StatusOK {
		t.Fatalf("rooms of a user named profiles = %d, want 200", code)
	}
}

// Without a profile store the routes answer 503 (never a silent default).
func TestProfileRoutesNeedStore(t *testing.T) {
	srv, _ := newTestServer(t)
	if code := closeStatus(t, do(t, http.MethodGet, srv.URL+"/v1/my/profile", "alice", nil)); code != http.StatusServiceUnavailable {
		t.Fatalf("no store = %d, want 503", code)
	}
}

// The public share page's "shared by" is the sharer's display name: the
// stored one when set, the id's local part otherwise.
func TestShareResolveUsesDisplayName(t *testing.T) {
	e, _, _ := sharesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "fileName": "spec.md", "content": "# v1"})
	_, link := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	if _, res := e.resolve(link.Token, false); res.SharedBy != (wire.ShareUser{UserID: "bob", DisplayName: "bob"}) {
		t.Fatalf("default shared by = %+v", res.SharedBy)
	}
	e.must(e.roomSrv, http.MethodPut, "/v1/my/profile", "bob", map[string]string{"displayName": "Bob B."})
	if _, res := e.resolve(link.Token, false); res.SharedBy != (wire.ShareUser{UserID: "bob", DisplayName: "Bob B."}) {
		t.Fatalf("named shared by = %+v", res.SharedBy)
	}
}
