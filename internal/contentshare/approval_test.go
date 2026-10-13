package contentshare

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Approval is the organization's setting alone, the same for every content
// kind; a new organization requires it, and an unset or unknown value
// fails closed.
func TestNeedsApproval(t *testing.T) {
	ts := store.NewMemoryTenant()
	if err := ts.PutTenant(context.Background(), store.Tenant{TenantID: "org", Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := ts.GetTenant(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsApproval(fresh) {
		t.Fatal("a new organization publishes without approval: the default is required")
	}
	for _, k := range contentpkg.Kinds() {
		for _, c := range []struct {
			setting string
			want    bool
		}{
			{wire.PublishApprovalRequired, true},
			{wire.PublishApprovalNone, false},
			{"", true},
			{"maybe", true},
		} {
			if got := NeedsApproval(store.Tenant{TenantID: "org", PublishApproval: c.setting}); got != c.want {
				t.Errorf("%s with publishApproval %q: needs approval = %v, want %v", k.Short, c.setting, got, c.want)
			}
		}
	}
}
