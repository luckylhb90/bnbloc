package membership

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// The member abilities fixture is shared with the web
// (web/src/lib/roomRoles.test.ts): both sides answer every row alike, and
// every room role has a row.
func TestAbilitiesFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "abilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Role      store.Role `json:"role"`
		AgentHost bool       `json:"agentHost"`
		IsHuman   bool       `json:"isHuman"`
		MayChat   bool       `json:"mayChat"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	roles := map[store.Role]bool{}
	for _, r := range rows {
		m := Member{Role: r.Role, AgentHost: r.AgentHost}
		if m.IsHuman() != r.IsHuman || m.MayChat() != r.MayChat {
			t.Errorf("%+v: IsHuman=%v MayChat=%v", r, m.IsHuman(), m.MayChat())
		}
		roles[r.Role] = true
	}
	for _, role := range []store.Role{store.RoleOwner, store.RoleAdmin, store.RoleMember, store.RoleVisitor} {
		if !roles[role] {
			t.Errorf("the fixture has no row for room role %q", role)
		}
	}
}
