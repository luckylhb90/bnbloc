package notify

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func testVAPID(t *testing.T) WebPushConfig {
	t.Helper()
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return WebPushConfig{PublicKey: pub, PrivateKey: priv, Subject: "mailto:ops@example.com"}
}

// browserKeys is a user agent's subscription key pair (RFC 8291 ua side).
type browserKeys struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowserKeys(t *testing.T) browserKeys {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return browserKeys{priv: priv, auth: auth}
}

func (k browserKeys) wire() wire.WebPushSubscriptionKeys {
	return wire.WebPushSubscriptionKeys{
		P256dh: base64.RawURLEncoding.EncodeToString(k.priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(k.auth),
	}
}

// decrypt opens an aes128gcm Web Push body as the browser would (RFC 8291 §3,
// RFC 8188), proving the channel encrypts to the subscription's keys.
func (k browserKeys) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	if rs < 18 {
		t.Fatalf("record size %d", rs)
	}
	peer, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := k.priv.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), k.priv.PublicKey().Bytes()...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, k.auth, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestWebPushConfigValidation(t *testing.T) {
	good := testVAPID(t)
	if err := good.Validate(); err != nil {
		t.Fatalf("generated pair: %v", err)
	}
	other := testVAPID(t)
	for name, c := range map[string]WebPushConfig{
		"public only":   {PublicKey: good.PublicKey, Subject: good.Subject},
		"mismatched":    {PublicKey: good.PublicKey, PrivateKey: other.PrivateKey, Subject: good.Subject},
		"not base64url": {PublicKey: "%%%", PrivateKey: good.PrivateKey, Subject: good.Subject},
		"no subject":    {PublicKey: good.PublicKey, PrivateKey: good.PrivateKey},
		"bad subject":   {PublicKey: good.PublicKey, PrivateKey: good.PrivateKey, Subject: "ops@example.com"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	t.Setenv(WebPushPublicKeyEnv, "")
	t.Setenv(WebPushPrivateKeyEnv, "")
	if c, err := WebPushConfigFromEnv(); err != nil || c.Enabled() {
		t.Fatalf("unset = %+v %v, want disabled", c, err)
	}
	t.Setenv(WebPushPublicKeyEnv, good.PublicKey)
	if _, err := WebPushConfigFromEnv(); err == nil {
		t.Fatal("one key without the other accepted")
	}
	t.Setenv(WebPushPrivateKeyEnv, good.PrivateKey)
	t.Setenv(WebPushSubjectEnv, "")
	t.Setenv(PublicURLEnv, "https://bnbloc.example.com/")
	c, err := WebPushConfigFromEnv()
	if err != nil || !c.Enabled() || c.Subject != "https://bnbloc.example.com" {
		t.Fatalf("pair + public URL = %+v %v", c, err)
	}
}

func TestCheckPublicAddr(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "100.64.0.1", "169.254.169.254", "::1", "fd00::1", "0.0.0.0"} {
		if checkPublicAddr("tcp", netip.MustParseAddr(a).String()+":443", nil) == nil {
			t.Errorf("%s allowed", a)
		}
	}
	if err := checkPublicAddr("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}

func pushRoutes(t *testing.T, p *WebPush) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	p.Register(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.NewContext(r.Context(), r.Header.Get("X-Test-Tenant"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebPushSubscriptionRoutes(t *testing.T) {
	subs := store.NewMemoryWebPush()
	keys := newBrowserKeys(t).wire()

	// Disabled channel: the key route and subscribe answer 404.
	off := pushRoutes(t, &WebPush{Subs: subs})
	if code := call(t, off, http.MethodGet, "/v1/push/vapid-public-key", "alice", nil, nil); code != http.StatusNotFound {
		t.Fatalf("disabled key route = %d", code)
	}
	if code := call(t, off, http.MethodPost, "/v1/my/push-subscriptions", "alice", wire.WebPushSubscriptionInput{Endpoint: "https://push.example/a", Keys: keys}, nil); code != http.StatusNotFound {
		t.Fatalf("disabled subscribe = %d", code)
	}

	cfg := testVAPID(t)
	srv := pushRoutes(t, &WebPush{Config: cfg, Subs: subs})
	var key wire.WebPushPublicKey
	if code := call(t, srv, http.MethodGet, "/v1/push/vapid-public-key", "alice", nil, &key); code != http.StatusOK || key.PublicKey != cfg.PublicKey {
		t.Fatalf("key = %d %+v", code, key)
	}
	if code := call(t, srv, http.MethodGet, "/v1/push/vapid-public-key", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous key = %d", code)
	}
	for name, in := range map[string]wire.WebPushSubscriptionInput{
		"http endpoint": {Endpoint: "http://push.example/a", Keys: keys},
		"no endpoint":   {Keys: keys},
		"bad p256dh":    {Endpoint: "https://push.example/a", Keys: wire.WebPushSubscriptionKeys{P256dh: "AAAA", Auth: keys.Auth}},
		"short auth":    {Endpoint: "https://push.example/a", Keys: wire.WebPushSubscriptionKeys{P256dh: keys.P256dh, Auth: "AAAA"}},
	} {
		if code := call(t, srv, http.MethodPost, "/v1/my/push-subscriptions", "alice", in, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	in := wire.WebPushSubscriptionInput{Endpoint: "https://push.example/a", Keys: keys}
	for i := 0; i < 2; i++ { // re-subscribing is idempotent
		if code := call(t, srv, http.MethodPost, "/v1/my/push-subscriptions", "alice", in, nil); code != http.StatusNoContent {
			t.Fatalf("subscribe = %d", code)
		}
	}
	rows, _ := subs.ListWebPushSubscriptions(tctx(), "alice")
	if len(rows) != 1 || rows[0].Endpoint != in.Endpoint || rows[0].P256dh != keys.P256dh || rows[0].ID != subscriptionID(in.Endpoint) {
		t.Fatalf("stored = %+v", rows)
	}
	// Another member cannot remove alice's subscription.
	if code := call(t, srv, http.MethodDelete, "/v1/my/push-subscriptions", "bob", wire.WebPushSubscriptionInput{Endpoint: in.Endpoint}, nil); code != http.StatusNoContent {
		t.Fatalf("foreign delete = %d", code)
	}
	if rows, _ := subs.ListWebPushSubscriptions(tctx(), "alice"); len(rows) != 1 {
		t.Fatal("foreign delete removed the subscription")
	}
	if code := call(t, srv, http.MethodDelete, "/v1/my/push-subscriptions", "alice", wire.WebPushSubscriptionInput{Endpoint: in.Endpoint}, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if rows, _ := subs.ListWebPushSubscriptions(tctx(), "alice"); len(rows) != 0 {
		t.Fatalf("after delete = %+v", rows)
	}
	// A member's devices are bounded: the oldest make room.
	for i := 0; i < maxPushSubscriptionsPerMember+3; i++ {
		ep := "https://push.example/dev-" + string(rune('a'+i))
		if code := call(t, srv, http.MethodPost, "/v1/my/push-subscriptions", "alice", wire.WebPushSubscriptionInput{Endpoint: ep, Keys: keys}, nil); code != http.StatusNoContent {
			t.Fatalf("subscribe %d = %d", i, code)
		}
	}
	if rows, _ := subs.ListWebPushSubscriptions(tctx(), "alice"); len(rows) != maxPushSubscriptionsPerMember {
		t.Fatalf("kept %d subscriptions, want %d", len(rows), maxPushSubscriptionsPerMember)
	}
}

// fakePushService is a push service: it records each request and answers
// with the status configured for the endpoint path.
type fakePushService struct {
	*httptest.Server
	mu     sync.Mutex
	status map[string]int
	got    map[string][][]byte
	auth   []string
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{status: map[string]int{}, got: map[string][][]byte{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got[r.URL.Path] = append(f.got[r.URL.Path], body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		code, ok := f.status[r.URL.Path]
		f.mu.Unlock()
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			code = http.StatusBadRequest
		} else if !ok {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePushService) setStatus(path string, code int) {
	f.mu.Lock()
	f.status[path] = code
	f.mu.Unlock()
}

func (f *fakePushService) bodies(path string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.got[path]...)
}

func subscribe(t *testing.T, subs store.WebPushSubscriptionStore, user, endpoint string, k browserKeys) {
	t.Helper()
	kw := k.wire()
	if err := subs.PutWebPushSubscription(tctx(), store.WebPushSubscription{
		ID: subscriptionID(endpoint), UserID: user, Endpoint: endpoint, P256dh: kw.P256dh, Auth: kw.Auth, CreatedAtMs: 1, UpdatedAtMs: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

// The emitter queues a push item for subscribed recipients only, with the
// same preference filtering as the notifier components — even when no
// notifier component is installed.
func TestEmitterQueuesWebPushForSubscribedRecipients(t *testing.T) {
	f := newFixture(t)
	subs := store.NewMemoryWebPush()
	f.emitter.Push = subs
	subscribe(t, subs, "bob", "https://push.example/bob", newBrowserKeys(t))
	subscribe(t, subs, "dave", "https://push.example/dave", newBrowserKeys(t))
	if err := f.mem.PutNotificationPrefs(tctx(), "dave", store.NotificationPrefs{Mentions: true, QuietRooms: []string{"r1"}}); err != nil {
		t.Fatal(err)
	}
	ok, err := f.emitter.Emit(tctx(), Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob", "carol", "dave"}, Actor: "alice", Title: "alice mentioned you", Text: "@bob hi", DedupKey: "msg:r1:3", Seq: 3})
	if !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	p, found := f.payload(t, wire.NotificationMention, "msg:r1:3", WebPushChannel)
	if !found || strings.Join(userIDs(p), ",") != "bob" || p.URL != "https://bnbloc.example.com/#/room/r1?seq=3" {
		t.Fatalf("push item = %+v found=%v", p, found)
	}
	if f.mem.Len() != 1 {
		t.Fatalf("outbox has %d items, want only the push item", f.mem.Len())
	}
}

func TestWorkerDeliversWebPushAndDropsGoneSubscriptions(t *testing.T) {
	f := newFixture(t)
	push := newFakePushService(t)
	subs := store.NewMemoryWebPush()
	f.emitter.Push = subs
	cfg := testVAPID(t)
	w := f.worker()
	w.Push = &WebPush{Config: cfg, Subs: subs, Client: push.Client()}
	w.MaxAttempts, w.BaseBackoff = 4, 10*time.Second

	laptop, phone, old := newBrowserKeys(t), newBrowserKeys(t), newBrowserKeys(t)
	subscribe(t, subs, "bob", push.URL+"/laptop", laptop)
	subscribe(t, subs, "bob", push.URL+"/phone", phone)
	subscribe(t, subs, "bob", push.URL+"/old", old)
	push.setStatus("/old", http.StatusGone)
	push.setStatus("/phone", http.StatusServiceUnavailable)

	if ok, err := f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"bob"}, Actor: "bob/codex", Title: "codex finished your request", Text: "All green.", DedupKey: "job:9", Seq: 12, ChannelID: "c1"}); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	id := EventID(tid, wire.NotificationAgentFinished, "job:9")

	// Attempt 1: laptop delivered, old gone (dropped), phone 503 -> retry.
	if n, err := w.DrainOnce(context.Background()); n != 1 || err != nil {
		t.Fatalf("drain = %d %v", n, err)
	}
	it, _ := f.mem.Item(tid, id, WebPushChannel)
	if it.Attempts != 1 || it.DeliveredAtMs != 0 || !strings.Contains(it.LastError, "503") {
		t.Fatalf("after attempt 1: %+v", it)
	}
	rows, _ := subs.ListWebPushSubscriptions(tctx(), "bob")
	if len(rows) != 2 {
		t.Fatalf("410 subscription not dropped: %+v", rows)
	}
	bodies := push.bodies("/laptop")
	if len(bodies) != 1 {
		t.Fatalf("laptop got %d messages", len(bodies))
	}
	var msg wire.WebPushMessage
	if err := json.Unmarshal(laptop.decrypt(t, bodies[0]), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != id || msg.Type != wire.NotificationAgentFinished || msg.Title != "codex finished your request in Design" || msg.Body != "All green." ||
		msg.URL != "https://bnbloc.example.com/#/room/r1?seq=12&channel=c1" || msg.Path != "#/room/r1?seq=12&channel=c1" {
		t.Fatalf("decrypted message = %+v", msg)
	}
	push.mu.Lock()
	vapid := push.auth[0]
	push.mu.Unlock()
	if !strings.HasPrefix(vapid, "vapid t=") || !strings.HasSuffix(vapid, ", k="+cfg.PublicKey) {
		t.Fatalf("Authorization = %q", vapid)
	}

	// Attempt 2 after the backoff: the phone recovers.
	f.clock.Add(13 * time.Second)
	push.setStatus("/phone", http.StatusCreated)
	if n, _ := w.DrainOnce(context.Background()); n != 1 {
		t.Fatal("due retry not claimed")
	}
	it, _ = f.mem.Item(tid, id, WebPushChannel)
	if it.DeliveredAtMs == 0 || it.Attempts != 2 {
		t.Fatalf("after attempt 2: %+v", it)
	}
	if got := push.bodies("/phone"); len(got) != 2 {
		t.Fatalf("phone got %d messages", len(got))
	} else if _, err := json.Marshal(phone.decrypt(t, got[1])); err != nil {
		t.Fatal(err)
	}
	if got := push.bodies("/old"); len(got) != 1 {
		t.Fatalf("dropped subscription contacted %d times", len(got))
	}
}

// A queued push item is given up when the deployment turned the channel off.
func TestWorkerGivesUpWebPushWhenDisabled(t *testing.T) {
	f := newFixture(t)
	subs := store.NewMemoryWebPush()
	f.emitter.Push = subs
	subscribe(t, subs, "bob", "https://push.example/bob", newBrowserKeys(t))
	if ok, err := f.emitter.Emit(tctx(), Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", Title: "t", DedupKey: "msg:r1:1"}); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	w := f.worker()
	w.Push = &WebPush{Subs: subs}
	if n, _ := w.DrainOnce(context.Background()); n != 1 {
		t.Fatal("not claimed")
	}
	it, _ := f.mem.Item(tid, EventID(tid, wire.NotificationMention, "msg:r1:1"), WebPushChannel)
	if it.FailedAtMs == 0 || !strings.Contains(it.LastError, "not configured") {
		t.Fatalf("item = %+v", it)
	}
}
