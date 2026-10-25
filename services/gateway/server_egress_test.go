package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/egress"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The platform's server egress: public by default; a super-admin sets
// the mode and the hosts (normalized, audited), a member cannot, a bad
// host is refused; the pages context fetches under it.
func TestServerEgressSettings(t *testing.T) {
	e, _, sink := assistGW(t)
	r := e.call(http.MethodGet, "/v1/platform/server-egress", nil, e.superAdmin())
	if r.status != http.StatusOK || r.json()["mode"] != wire.ServerEgressPublic {
		t.Fatalf("default = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPut, "/v1/platform/server-egress", wire.ServerEgress{Mode: wire.ServerEgressAllowlist}, e.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member sets it: %d", r.status)
	}
	for _, bad := range []wire.ServerEgress{{Mode: "open"}, {Mode: wire.ServerEgressPublic, Hosts: []string{"https://docs.example.com"}}, {Mode: wire.ServerEgressPublic, Hosts: []string{"*.example.com"}}} {
		if r := e.call(http.MethodPut, "/v1/platform/server-egress", bad, e.superAdmin()); r.status != http.StatusBadRequest {
			t.Fatalf("%+v: %d %s", bad, r.status, r.body)
		}
	}
	r = e.call(http.MethodPut, "/v1/platform/server-egress", wire.ServerEgress{Mode: wire.ServerEgressAllowlist, Hosts: []string{" Docs.Example.com", "docs.example.com", "api.example.com"}}, e.superAdmin())
	if r.status != http.StatusOK || strings.Join(toStrings(r.json()["hosts"]), ",") != "api.example.com,docs.example.com" || r.json()["updatedBy"] != "ops" {
		t.Fatalf("save = %d %s", r.status, r.body)
	}
	recs := sink.take()
	if len(recs) != 1 || recs[0].Kind != wire.AuditKindNetwork || !strings.Contains(recs[0].Detail, "allowlist, hosts api.example.com, docs.example.com") {
		t.Fatalf("audit = %+v", recs)
	}
	if ps := assist.FetchPages(e.ctx(), e.gw.pagesClient(e.ctx()), []string{"https://elsewhere.example.org/doc"}); ps[0].Error != egress.ErrNotListed.Error() {
		t.Fatalf("an unlisted page under the allowlist = %+v", ps)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
