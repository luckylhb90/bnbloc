package membership

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A preview (a non-member of a shared room) passes Read and nothing else, and
// is no person in the room (docs/prd/2026-10-22-shared-room-preview.md).
func TestPreviewPassesOnlyRead(t *testing.T) {
	s := NewStatic()
	s.SetRoomTenant("r1", "org-a")
	s.Add("r1", "maya")
	s.SetShared("r1", true)
	ctx := tenant.NewContext(context.Background(), "org-a")

	m, err := RoomScope(ctx, s, "r1", "jack", Read)
	if err != nil {
		t.Fatalf("Read of a shared room by a non-member: %v", err)
	}
	if !m.Preview || m.IsHuman() || m.MayChat() || m.Tenant != "org-a" || m.Role != store.RoleVisitor {
		t.Fatalf("preview = %+v, want a read-only non-person of org-a", m)
	}
	for name, need := range map[string]Need{"AnyMember": AnyMember, "Human": Human, "Chat": Chat} {
		if _, err := RoomScope(ctx, s, "r1", "jack", need); !errors.Is(err, ErrNotMember) {
			t.Errorf("%s by a preview: err = %v, want ErrNotMember", name, err)
		}
	}
	// A member is a member under Read, not a preview.
	if m, err := RoomScope(ctx, s, "r1", "maya", Read); err != nil || m.Preview || !m.IsHuman() {
		t.Fatalf("member under Read = %+v, %v", m, err)
	}
}

func TestPreviewNeedsASharedRoomInTheRequestsTenant(t *testing.T) {
	s := NewStatic()
	s.SetRoomTenant("r1", "org-a")
	ctx := tenant.NewContext(context.Background(), "org-a")
	if _, err := RoomScope(ctx, s, "r1", "jack", Read); !errors.Is(err, ErrNotMember) {
		t.Fatalf("a room that is not shared: err = %v, want ErrNotMember", err)
	}
	s.SetShared("r1", true)
	other := tenant.NewContext(context.Background(), "org-b")
	if _, err := RoomScope(other, s, "r1", "jack", Read); !errors.Is(err, ErrOtherTenant) {
		t.Fatalf("a request signed for another tenant: err = %v, want ErrOtherTenant", err)
	}
	if _, err := RoomScope(ctx, nil, "r1", "jack", Read); !errors.Is(err, ErrNotMember) {
		t.Fatalf("nil directory: err = %v, want ErrNotMember", err)
	}
}

// A directory that is no Previewer has no previews: fail closed.
func TestPreviewOfADirectoryThatCannotPreview(t *testing.T) {
	var d Directory = noPreview{NewStatic()}
	if _, ok := PreviewOf(d, "r1", "jack"); ok {
		t.Fatal("a directory without Previewer answered a preview")
	}
}

// noPreview hides Static's Preview: only Directory's methods remain.
type noPreview struct{ s *Static }

func (n noPreview) Lookup(r, u string) (Member, bool) { return n.s.Lookup(r, u) }
func (n noPreview) Fresh(r, u string) (Member, bool)  { return n.s.Fresh(r, u) }
func (n noPreview) Forget(r, u string)                { n.s.Forget(r, u) }

// The room client reads a preview from the probe's {"preview":true}, caches
// it like a membership, drops it on Forget, and never takes one for a
// membership.
func TestRoomClientPreview(t *testing.T) {
	var calls int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)

	body = `{"member":false,"preview":true,"tenantId":"org-a"}`
	m, ok := c.Preview("r1", "jack")
	if !ok || !m.Preview || m.Tenant != "org-a" || m.Role != store.RoleVisitor {
		t.Fatalf("preview = %+v %v", m, ok)
	}
	if _, ok := c.Lookup("r1", "jack"); ok {
		t.Fatal("a preview is no membership: Lookup answered one")
	}
	calls = 0
	if _, ok := c.Preview("r1", "jack"); !ok || calls != 0 {
		t.Fatalf("cached preview: ok=%v calls=%d, want 0 calls", ok, calls)
	}
	c.Forget("r1", "jack")
	if _, ok := c.Preview("r1", "jack"); !ok || calls != 1 {
		t.Fatalf("after Forget: calls = %d, want 1", calls)
	}

	for _, b := range []string{
		`{"member":false}`, // private room
		`{"member":true,"role":"member","tenantId":"org-a","preview":true}`, // a member
		`{"member":false,"preview":true}`,                                   // no tenant
	} {
		body = b
		c.Forget("r1", "")
		if _, ok := c.Preview("r1", "jack"); ok {
			t.Errorf("probe %s answered a preview", b)
		}
	}
}
