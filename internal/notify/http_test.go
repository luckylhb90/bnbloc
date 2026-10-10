package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func newHandlerServer(t *testing.T, f *fixture) (*httptest.Server, *Handlers) {
	t.Helper()
	if f == nil {
		f = newFixture(t)
	}
	h := &Handlers{
		Prefs: f.mem, Notifiers: f.dir, Installs: f.installs, Secrets: f.secrets,
		PublicURL: "https://bnbloc.example.com", Audit: f.audit, Now: f.clock.Now,
	}
	mux := http.NewServeMux()
	h.Register(mux)
	// Stand-in for svcauth: the tenant arrives from the signed header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.NewContext(r.Context(), r.Header.Get("X-Test-Tenant"))))
	}))
	t.Cleanup(srv.Close)
	return srv, h
}

func call(t *testing.T, srv *httptest.Server, method, path, user string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if user != "" {
		req.Header.Set("X-Bnbloc-User", user)
	}
	req.Header.Set("X-Test-Tenant", tid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestPrefsEndpoints(t *testing.T) {
	srv, _ := newHandlerServer(t, nil)
	var p wire.NotificationPrefs
	if code := call(t, srv, "GET", "/v1/my/notification-prefs", "bob", nil, &p); code != 200 {
		t.Fatalf("get = %d", code)
	}
	if !(p.Mentions && p.AgentFinished && p.AgentFailed && p.BorrowRequests) || p.QuietRooms == nil || len(p.QuietRooms) != 0 {
		t.Fatalf("defaults = %+v, want all on and empty quietRooms", p)
	}
	put := wire.NotificationPrefs{Mentions: true, AgentFailed: true, QuietRooms: []string{"r1", "r1", " r2 "}}
	if code := call(t, srv, "PUT", "/v1/my/notification-prefs", "bob", put, &p); code != 200 {
		t.Fatalf("put = %d", code)
	}
	if p.AgentFinished || p.BorrowRequests || !reflect.DeepEqual(p.QuietRooms, []string{"r1", "r2"}) {
		t.Fatalf("saved = %+v", p)
	}
	var again wire.NotificationPrefs
	call(t, srv, "GET", "/v1/my/notification-prefs", "bob", nil, &again)
	if !reflect.DeepEqual(again, p) {
		t.Fatalf("read back %+v, want %+v", again, p)
	}
	// Prefs are per member.
	var alice wire.NotificationPrefs
	call(t, srv, "GET", "/v1/my/notification-prefs", "alice", nil, &alice)
	if !alice.AgentFinished {
		t.Fatal("bob's prefs leaked to alice")
	}
	if code := call(t, srv, "PUT", "/v1/my/notification-prefs", "bob", wire.NotificationPrefs{QuietRooms: []string{"bad/room"}}, nil); code != 400 {
		t.Fatalf("invalid room id = %d, want 400", code)
	}
	if code := call(t, srv, "GET", "/v1/my/notification-prefs", "", nil, nil); code != 401 {
		t.Fatalf("anonymous = %d, want 401", code)
	}
}

func findNotifier(list []wire.MemberNotifier, name string) (wire.MemberNotifier, bool) {
	for _, n := range list {
		if n.Name == name {
			return n, true
		}
	}
	return wire.MemberNotifier{}, false
}

func TestMemberNotifierEndpoints(t *testing.T) {
	var mu sync.Mutex
	var sent wire.NotificationPayload
	receiver := fakeNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&sent)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(wire.NotificationDeliveryResponse{Results: []wire.NotificationRecipientResult{{UserID: "bob", OK: true}}})
	})
	f := newFixture(t)
	f.install(t, "n1", receiver.URL+"/hook", "")
	f.install(t, "opt", receiver.URL+"/hook", extpipe.RolloutOptIn)
	f.install(t, "hidden", receiver.URL+"/hook", extpipe.RolloutOff)
	srv, _ := newHandlerServer(t, f)

	var list struct{ Notifiers []wire.MemberNotifier }
	if code := call(t, srv, "GET", "/v1/my/notifiers", "bob", nil, &list); code != 200 {
		t.Fatalf("list = %d", code)
	}
	if n, ok := findNotifier(list.Notifiers, "n1"); !ok || n.OptIn || !n.Installed {
		t.Fatalf("n1 = %+v ok=%v", n, ok)
	}
	if n, ok := findNotifier(list.Notifiers, "opt"); !ok || !n.OptIn || n.Installed {
		t.Fatalf("opt = %+v ok=%v", n, ok)
	}
	if _, ok := findNotifier(list.Notifiers, "hidden"); ok {
		t.Fatal("rollout-off notifier listed")
	}
	// Notifiers carry no member settings any more: the routes are gone.
	if code := call(t, srv, "PUT", "/v1/my/notifiers/n1/settings", "bob", map[string]any{"values": map[string]string{"appId": "a"}}, nil); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Fatalf("settings route = %d, want gone", code)
	}
	// Test send: through the notifier's own delivery, to the member only.
	var res wire.NotificationTestResult
	if code := call(t, srv, "POST", "/v1/my/notifiers/n1/test", "bob", nil, &res); code != 200 || !res.Delivered {
		t.Fatalf("test = %d %+v", code, res)
	}
	mu.Lock()
	if sent.Type != wire.NotificationTest || len(sent.Recipients) != 1 || sent.Recipients[0].UserID != "bob" {
		t.Fatalf("test payload = %+v", sent)
	}
	mu.Unlock()
	if code := call(t, srv, "POST", "/v1/my/notifiers/hidden/test", "bob", nil, nil); code != 404 {
		t.Fatalf("hidden notifier test = %d, want 404", code)
	}
}

func rawGet(t *testing.T, srv *httptest.Server, path, user string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("X-Bnbloc-User", user)
	req.Header.Set("X-Test-Tenant", tid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
