package shareagenthttp

import (
	"context"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// dctx scopes direct-store seeds to the default tenant, matching what the
// service Handler (svcauth.EnsureTenant) injects for HTTP requests.
func dctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func newService(t *testing.T) (*Service, *store.MemoryGrant) {
	t.Helper()
	grants := store.NewMemoryGrant()
	members := membership.NewStatic()
	for _, u := range []string{"alice", "bob", "jack", "reader"} {
		members.Add("room-1", u)
	}
	svc := &Service{
		Grants:     grants,
		Membership: members,
		Now:        func() int64 { return 1000 },
	}
	return svc, grants
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	return resp
}
