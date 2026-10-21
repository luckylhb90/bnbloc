package room

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Reading preferences (docs/prd/2026-10-21-feed-by-request.md §3.1): the
// time layout until the member chooses, the stored choice after (per
// person), a layout outside the union refused, an authenticated caller
// required.
func TestMyPreferences(t *testing.T) {
	base, _ := newProfileServer(t)
	const me, other = "jane.d@example.com", "bob"
	get := func(user string) wire.MemberPreferences {
		t.Helper()
		resp := do(t, http.MethodGet, base+"/v1/my/preferences", user, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get = %d", closeStatus(t, resp))
		}
		return decodeInto[wire.MemberPreferences](t, resp)
	}
	if p := get(me); p.FeedLayout != wire.FeedLayoutTime {
		t.Fatalf("default = %+v", p)
	}
	resp := do(t, http.MethodPut, base+"/v1/my/preferences", me, map[string]string{"feedLayout": "request"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d", closeStatus(t, resp))
	}
	if p := decodeInto[wire.MemberPreferences](t, resp); p.FeedLayout != wire.FeedLayoutRequest {
		t.Fatalf("put answer = %+v", p)
	}
	if p := get(me); p.FeedLayout != wire.FeedLayoutRequest {
		t.Fatalf("read back = %+v", p)
	}
	if p := get(other); p.FeedLayout != wire.FeedLayoutTime {
		t.Fatalf("another member = %+v, want their own default", p)
	}
	for _, bad := range []map[string]string{{"feedLayout": ""}, {"feedLayout": "Request"}, {"feedLayout": "thread"}, {}} {
		if code := closeStatus(t, do(t, http.MethodPut, base+"/v1/my/preferences", me, bad)); code != http.StatusBadRequest {
			t.Fatalf("PUT %v = %d, want 400", bad, code)
		}
	}
	if p := get(me); p.FeedLayout != wire.FeedLayoutRequest {
		t.Fatalf("after refused writes = %+v, want unchanged", p)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if code := closeStatus(t, do(t, method, base+"/v1/my/preferences", "", map[string]string{"feedLayout": "time"})); code != http.StatusUnauthorized {
			t.Fatalf("%s without a subject = %d, want 401", method, code)
		}
	}
}
