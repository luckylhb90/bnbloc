package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/componentcred"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// F5 end to end through realtime: a notifier component installed through
// the component pipeline is activated by realtime; a chat that @-mentions a
// member is published, observed off the publish path, enqueued per notifier,
// and delivered signed with the component's secret; the member notifier
// routes are served by the same service.
// testCredentialBox is the secret store key the notifiers' credentials
// are sealed with (realtime opens them to sign deliveries).
func testCredentialBox(t *testing.T) *sealbox.Box {
	t.Helper()
	box, err := sealbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestChatMentionDeliversToNotifierComponent(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	var sigs []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/n/discover" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp", "contract": pkgmeta.NotifierContractV1})
			return
		}
		if r.URL.Path != "/n/v1/notifications" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		sigs = append(sigs, r.Header.Get(wire.NotificationSignatureHeader))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	notes := store.NewMemoryNotifications()
	settings := store.NewMemoryPlatformSettings()
	box := testCredentialBox(t)
	ns := notify.NewService(notify.Stores{
		Prefs: notes, Outbox: notes, Settings: settings, Audit: store.NewMemoryExtensionAudit(), Box: box,
	}, mem, notify.Options{PublicURL: "https://bnbloc.example.com"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := NewComponentPipeline(settings, nil, nil, nil, nil, ns.Notifiers.Installer())
	pipe.AllowUnsigned = true
	pkg, _ := json.Marshal(map[string]any{
		"kind": pkgmeta.KindNotifierPackageV1, "name": "corp", "version": "0.1.0", "publisher": "test",
		"runtime": "endpoint", "contract": pkgmeta.NotifierContractV1,
		"endpoint": map[string]any{"baseUrl": receiver.URL + "/n"}, "permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if _, err := pipe.Install(ctx, extpipe.InstallRequest{Body: pkg, Actor: "root"}); err != nil {
		t.Fatalf("install notifier: %v", err)
	}
	secret, _, err := (&componentcred.Store{Settings: settings, Box: box}).Ensure(ctx, "notifier", "corp", "root")
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)

	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(64, 16), Membership: mem, Notifications: ns}).Handler())
	defer srv.Close()
	do := func(method, path, user string, body any, out any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("X-Bnbloc-User", user)
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

	var list struct{ Notifiers []wire.MemberNotifier }
	if code := do("GET", "/v1/my/notifiers", "bob", nil, &list); code != 200 || len(list.Notifiers) != 1 || list.Notifiers[0].Name != "corp" {
		t.Fatalf("my notifiers = %d %+v", code, list)
	}
	if code := do("POST", "/v1/rooms/room-1/chat", "alice", map[string]any{"text": "@bob please review"}, nil); code != 200 {
		t.Fatalf("chat = %d", code)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("receiver got %d deliveries, want 1", len(bodies))
	}
	if err := notify.Verify(sigs[0], secret, bodies[0], time.Now(), notify.SignatureTolerance); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var p wire.NotificationPayload
	_ = json.Unmarshal(bodies[0], &p)
	if p.Type != wire.NotificationMention || len(p.Recipients) != 1 || p.Recipients[0].UserID != "bob" || p.Actor != "alice" ||
		p.TenantID != tenant.Default || !strings.HasPrefix(p.URL, "https://bnbloc.example.com/#/room/room-1?seq=") {
		t.Fatalf("payload = %+v", p)
	}
}
