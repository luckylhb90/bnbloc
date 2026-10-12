package extpipe

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestMemberVisibilityExecutableRecords(t *testing.T) {
	maya := Viewer{Subject: "maya", Tenant: "t1"}
	for name, c := range map[string]struct {
		rec       Record
		installed int64
		want      Visibility
	}{
		"auto":              {Record{}, 0, Visibility{Visible: true, Installed: true, Managed: true}},
		"off":               {Record{Rollout: RolloutOff}, 0, Visibility{}},
		"optin listed":      {Record{Rollout: RolloutOptIn}, 0, Visibility{Visible: true}},
		"optin installed":   {Record{Rollout: RolloutOptIn}, 5, Visibility{Visible: true, Installed: true, InstalledAtMs: 5}},
		"other tenant only": {Record{EnabledTenants: []string{"t2"}}, 0, Visibility{}},
		"listed tenant":     {Record{EnabledTenants: []string{"t1"}}, 0, Visibility{Visible: true, Installed: true, Managed: true}},
	} {
		if got := MemberVisibility(maya, GateForRecord(c.rec), c.installed); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

func TestMemberVisibilityContent(t *testing.T) {
	maya := Viewer{Subject: "maya", Tenant: "t1"}
	jack := Viewer{Subject: "jack", Tenant: "t1"}
	mayaOther := Viewer{Subject: "maya", Tenant: "t2"}
	personal := store.ContentComponent{Owner: "maya", TenantID: "t1", Scope: store.ContentScopePersonal, Status: store.ContentStatusActive}
	use := Visibility{Visible: true, Installed: true}
	if got := MemberVisibility(maya, GateForContent(personal), 0); got != use {
		t.Fatalf("author = %+v", got)
	}
	if got := MemberVisibility(jack, GateForContent(personal), 0); got != (Visibility{}) {
		t.Fatalf("other member = %+v", got)
	}
	if got := MemberVisibility(mayaOther, GateForContent(personal), 0); got != (Visibility{}) {
		t.Fatalf("other tenant = %+v", got)
	}
	if got := MemberVisibility(Viewer{Tenant: "t1"}, GateForContent(store.ContentComponent{Scope: store.ContentScopePersonal, TenantID: "t1"}), 0); got != (Visibility{}) {
		t.Fatalf("empty subject = %+v", got)
	}
	retired := personal
	retired.Status = store.ContentStatusRetired
	if got := MemberVisibility(maya, GateForContent(retired), 0); got != (Visibility{}) {
		t.Fatalf("retired = %+v", got)
	}
	pending := store.ContentComponent{Owner: "maya", TenantID: "t1", Scope: store.ContentScopeOrg, Status: store.ContentStatusPending}
	if got := MemberVisibility(maya, GateForContent(pending), 0); got != (Visibility{Visible: true}) {
		t.Fatalf("pending author = %+v", got)
	}
	if got := MemberVisibility(jack, GateForContent(pending), 0); got != (Visibility{}) {
		t.Fatalf("pending other = %+v", got)
	}
	org := store.ContentComponent{Owner: "maya", TenantID: "t1", Scope: store.ContentScopeOrg, Status: store.ContentStatusActive}
	if got := MemberVisibility(jack, GateForContent(org), 0); !got.Installed {
		t.Fatalf("org member = %+v", got)
	}
	if got := MemberVisibility(Viewer{Subject: "kim", Tenant: "t9"}, GateForContent(org), 0); got != (Visibility{}) {
		t.Fatalf("org outsider = %+v", got)
	}
}
