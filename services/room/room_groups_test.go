package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func (e *appEnv) roomDo(user, method, path string, body any) reply {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := svcauth.Key(appTestKey).NewRequest(context.Background(), method, e.srv.URL+path, bytes.NewReader(raw), user, e.tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: out}
}

// Members who may act ask for connect codes (bounded, names checked);
// visitors and outsiders cannot; members read the room's groups.
func TestRoomGroupConnectRules(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.RoomGroups = b.RoomGroups
		base := "/v1/rooms/" + e.room + "/groups"
		r := e.roomDo("jack", http.MethodPost, base+"/connect", wire.RoomGroupConnectRequest{Name: "weekly"})
		var c wire.RoomGroupConnect
		if r.status != http.StatusCreated || json.Unmarshal(r.body, &c) != nil || len(c.Code) != len(wire.RoomGroupCodePrefix)+8 || c.ExpiresAtMs <= e.svc.Now() {
			t.Fatalf("connect = %d %s", r.status, r.body)
		}
		for _, who := range []string{"vic", "zed"} {
			if r := e.roomDo(who, http.MethodPost, base+"/connect", wire.RoomGroupConnectRequest{Name: "x"}); r.status != http.StatusForbidden {
				t.Fatalf("%s connects = %d", who, r.status)
			}
		}
		for _, bad := range []string{"", "a:b", string(make([]byte, 70))} {
			if r := e.roomDo("jack", http.MethodPost, base+"/connect", wire.RoomGroupConnectRequest{Name: bad}); r.status != http.StatusBadRequest {
				t.Fatalf("name %q = %d", bad, r.status)
			}
		}
		for i := 0; i < wire.MaxRoomGroupCodesOpen-1; i++ {
			e.roomDo("jack", http.MethodPost, base+"/connect", wire.RoomGroupConnectRequest{Name: "weekly"})
		}
		if r := e.roomDo("jack", http.MethodPost, base+"/connect", wire.RoomGroupConnectRequest{Name: "weekly"}); r.status != http.StatusTooManyRequests {
			t.Fatalf("a code over the open cap = %d", r.status)
		}
		if r := e.roomDo("vic", http.MethodGet, base, nil); r.status != http.StatusOK {
			t.Fatalf("a visitor lists = %d", r.status)
		}
		if r := e.roomDo("zed", http.MethodGet, base, nil); r.status != http.StatusForbidden {
			t.Fatalf("an outsider lists = %d", r.status)
		}
	})
}
