package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/sharetarget"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const testHostToken = "host-token-SECRET-value"

const sharePageDoc = "<!doctype html><title>share page</title><script>/* the host app's calls */</script>"

func shareTargetPackageBody(t *testing.T, name, baseURL string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindShareTargetPackageV1, "name": name, "version": "0.1.0", "publisher": "Acme Corp",
		"runtime": pkgmeta.RuntimeEndpoint, "contract": pkgmeta.ShareTargetContractV1,
		"endpoint":    map[string]any{"baseUrl": baseURL},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// recordingAudit keeps every audit row the gateway writes.
type recordingAudit struct {
	mu   sync.Mutex
	recs []store.ExtAuditRecord
}

func (a *recordingAudit) AppendExtAudit(_ context.Context, rec store.ExtAuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
	return nil
}

// shareRoomBackend answers the reads a share makes as the member: room
// "r1" (a member's), its deliverable "a1" and one message at seq 7. Any
// other room is the room service's 404.
func shareRoomBackend(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "r1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"roomId": "r1", "name": "Launch **plan**", "description": "Where the launch is planned"})
	})
	mux.HandleFunc("GET /v1/rooms/{id}/artifacts/{aid}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "r1" || r.PathValue("aid") != "a1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(wire.ArtifactDetail{
			Artifact: wire.Artifact{ArtifactID: "a1", RoomID: "r1", Title: "Roadmap", CurrentVersion: 2},
			Versions: []wire.ArtifactVersion{{ArtifactID: "a1", Version: 2}},
		})
	})
	mux.HandleFunc("GET /v1/rooms/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "r1" {
			http.NotFound(w, r)
			return
		}
		// "the newest row below before": 7 is answered for before=8, and
		// an earlier row for any other, as realtime does.
		seq := int64(6)
		if r.URL.Query().Get("before") == "8" {
			seq = 7
		}
		payload, _ := json.Marshal(map[string]any{"userId": "maya", "text": "## Ship it\n[the doc](https://x.example) is ready"})
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []roomlog.Row{{Sequence: seq, ChannelID: "c1", Kind: wire.KindChatMessage, Payload: payload}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type shareFixture struct {
	svc    *Service
	srv    *httptest.Server
	token  string
	secret string
	audit  *recordingAudit
	shares chan sharetarget.Share
	sigs   chan string
	raws   chan []byte
	status *int // what the target answers to a share
	reason *string
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	fx := &shareFixture{shares: make(chan sharetarget.Share, 8), sigs: make(chan string, 8), raws: make(chan []byte, 8)}
	status, reason := http.StatusNoContent, ""
	fx.status, fx.reason = &status, &reason
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share/discover":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp-share", "contract": pkgmeta.ShareTargetContractV1})
		case "/share/v1/share/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(sharePageDoc))
		case "/share/v1/share":
			raw, _ := io.ReadAll(r.Body)
			var s sharetarget.Share
			_ = json.Unmarshal(raw, &s)
			fx.shares <- s
			fx.sigs <- r.Header.Get(wire.ShareSignatureHeader)
			fx.raws <- raw
			if *fx.status >= 300 {
				w.WriteHeader(*fx.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"reason": *fx.reason})
				return
			}
			w.WriteHeader(*fx.status)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)
	svc, srv, token, key := signedPackageGatewayConfigured(t, true, false, true)
	fx.audit = &recordingAudit{}
	svc.ExtAudit = fx.audit
	room := shareRoomBackend(t)
	svc.Backends.Room, svc.Backends.Realtime = room.URL, room.URL
	body := shareTargetPackageBody(t, "corp-share", target.URL+"/share")
	raw, code := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if code != http.StatusOK || installed.Kind != "share-target" || !strings.HasPrefix(installed.Secret, "whsec_") {
		t.Fatalf("install = %d %s", code, raw)
	}
	fx.svc, fx.srv, fx.token, fx.secret = svc, srv, token, installed.Secret
	return fx
}

func (fx *shareFixture) do(t *testing.T, method, url string, body any, bearer string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// startSession starts a share as the member and returns the page's address.
func (fx *shareFixture) startSession(t *testing.T, name string, item wire.ShareItem) (string, []byte, int) {
	t.Helper()
	resp, raw := fx.do(t, http.MethodPost, fx.srv.URL+"/v1/my/share-targets/"+name+"/sessions", wire.ShareSessionRequest{Item: item}, fx.token)
	var s wire.ShareSession
	_ = json.Unmarshal(raw, &s)
	return s.URL, raw, resp.StatusCode
}

// sendTo posts a send to the page at pageURL (the page's own address + "send").
func (fx *shareFixture) sendTo(t *testing.T, pageURL string, req wire.ShareSend) ([]byte, int) {
	t.Helper()
	resp, raw := fx.do(t, http.MethodPost, pageURL+"send", req, "")
	return raw, resp.StatusCode
}

func shareDest() []wire.ShareDest {
	return []wire.ShareDest{{ID: "+1001", Type: wire.ShareDestUser}, {ID: "8bd2e98b1d7a4766bef6dfedcb372a03", Type: wire.ShareDestGroup}}
}

func (fx *shareFixture) received(t *testing.T) sharetarget.Share {
	t.Helper()
	select {
	case s := <-fx.shares:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("the target received nothing")
		return sharetarget.Share{}
	}
}

func (fx *shareFixture) calls() int { return len(fx.shares) }

// A member sees the target; each item kind becomes a session whose page the
// gateway serves sandboxed, whose session names the card, and whose send
// reaches the target signed, as that member, with the card the member
// authorized and the conversations the page chose.
func TestShareTargetSessionPageAndSend(t *testing.T) {
	fx := newShareFixture(t)
	resp, raw := fx.do(t, http.MethodGet, fx.srv.URL+"/v1/my/share-targets", nil, fx.token)
	var list wire.ShareTargetList
	_ = json.Unmarshal(raw, &list)
	if resp.StatusCode != http.StatusOK || len(list.Targets) != 1 || list.Targets[0].Name != "corp-share" {
		t.Fatalf("targets = %d %s", resp.StatusCode, raw)
	}
	cases := []struct {
		name                string
		item                wire.ShareItem
		title, summary, url string
	}{
		{"room", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"},
			"Launch plan", "Where the launch is planned", "/#/room/r1"},
		{"deliverable", wire.ShareItem{Kind: wire.ShareItemDeliverable, RoomID: "r1", ArtifactID: "a1", Version: 2},
			"Roadmap", "Launch plan", "/#/room/r1?artifact=a1&v=2"},
		{"message", wire.ShareItem{Kind: wire.ShareItemMessage, RoomID: "r1", ChannelID: "c1", Seq: 7},
			"Launch plan", "Ship it the doc is ready", "/#/room/r1?seq=7&channel=c1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, raw, code := fx.startSession(t, "corp-share", c.item)
			if code != http.StatusOK || !strings.HasPrefix(page, fx.srv.URL+"/v1/share-targets/corp-share/pages/") || !strings.HasSuffix(page, "/") {
				t.Fatalf("session = %d %s", code, raw)
			}
			// The page: the component's document, sandboxed, never cached.
			presp, body := fx.do(t, http.MethodGet, page, nil, "")
			if presp.StatusCode != http.StatusOK || string(body) != sharePageDoc {
				t.Fatalf("page = %d %q", presp.StatusCode, body)
			}
			for k, want := range map[string]string{
				"Content-Security-Policy": "sandbox allow-scripts allow-forms allow-modals", "Referrer-Policy": "no-referrer",
				"Cache-Control": "no-store", "Access-Control-Allow-Origin": "*",
			} {
				if got := presp.Header.Get(k); got != want {
					t.Errorf("page header %s = %q, want %q", k, got, want)
				}
			}
			if presp.Header.Get("Access-Control-Allow-Credentials") != "" {
				t.Error("the share page answers credentialed CORS")
			}
			// The session: what the page shows and where it sends.
			_, raw = fx.do(t, http.MethodGet, page+"session", nil, "")
			var info wire.ShareSessionInfo
			_ = json.Unmarshal(raw, &info)
			if info.Card.Title != c.title || info.Card.Summary != c.summary || !strings.HasSuffix(info.Card.URL, c.url) || info.BackURL != info.Card.URL || info.SendURL != page+"send" {
				t.Fatalf("session info = %s", raw)
			}
			// The send: by ticket alone, no session.
			raw, code = fx.sendTo(t, page, wire.ShareSend{DeliveryID: "delivery-1", Dest: shareDest(), HostToken: testHostToken})
			if code != http.StatusOK {
				t.Fatalf("send = %d %s", code, raw)
			}
			got := fx.received(t)
			if got.Card.Kind != c.item.Kind || got.Card.Title != c.title || got.Card.Summary != c.summary || !strings.HasSuffix(got.Card.URL, c.url) {
				t.Fatalf("card = %+v", got.Card)
			}
			if got.DeliveryID != "delivery-1" || len(got.Dest) != 2 || got.Sharer.Subject == "" || got.HostToken != testHostToken {
				t.Fatalf("share = %+v", got)
			}
			if err := notify.Verify(<-fx.sigs, fx.secret, <-fx.raws, time.Now(), notify.SignatureTolerance); err != nil {
				t.Fatalf("signature: %v", err)
			}
		})
	}
	var delivered int
	for _, rec := range fx.audit.recs {
		if rec.Kind == "share-target" && rec.Action == "share" && rec.Outcome == wire.AuditOutcomeDelivered && rec.Actor != "" {
			delivered++
		}
	}
	if delivered != len(cases) {
		t.Fatalf("delivered share audit rows = %d in %+v", delivered, fx.audit.recs)
	}
}

// An item the member cannot read never gets a session, and neither does a
// malformed item or a name that is not an installed target.
func TestShareTargetSessionRefusals(t *testing.T) {
	fx := newShareFixture(t)
	room := wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"}
	for name, tc := range map[string]struct {
		target string
		item   wire.ShareItem
		want   int
	}{
		"another room":        {"corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "elsewhere"}, http.StatusNotFound},
		"no such deliverable": {"corp-share", wire.ShareItem{Kind: wire.ShareItemDeliverable, RoomID: "r1", ArtifactID: "nope"}, http.StatusNotFound},
		"no such message":     {"corp-share", wire.ShareItem{Kind: wire.ShareItemMessage, RoomID: "r1", Seq: 9}, http.StatusNotFound},
		"no such target":      {"other", room, http.StatusNotFound},
		"unknown kind":        {"corp-share", wire.ShareItem{Kind: "page", RoomID: "r1"}, http.StatusBadRequest},
		"message without seq": {"corp-share", wire.ShareItem{Kind: wire.ShareItemMessage, RoomID: "r1"}, http.StatusBadRequest},
		"long id":             {"corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: strings.Repeat("r", wire.MaxShareIDBytes+1)}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if _, raw, code := fx.startSession(t, tc.target, tc.item); code != tc.want {
				t.Fatalf("session = %d %s, want %d", code, raw, tc.want)
			}
		})
	}
	// Starting a session needs a member's session.
	if resp, _ := fx.do(t, http.MethodPost, fx.srv.URL+"/v1/my/share-targets/corp-share/sessions", wire.ShareSessionRequest{Item: room}, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session = %d, want 401", resp.StatusCode)
	}
	if n := fx.calls(); n != 0 {
		t.Fatalf("the target was called %d times", n)
	}
}

// The ticket is the page's only credential: a bad, expired, other-kind or
// other-target ticket, and a malformed send, never reach the target.
func TestShareTicketRefusals(t *testing.T) {
	fx := newShareFixture(t)
	page, _, code := fx.startSession(t, "corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"})
	if code != http.StatusOK {
		t.Fatalf("session = %d", code)
	}
	card, _ := json.Marshal(wire.ShareCard{Kind: wire.ShareItemRoom, Title: "forged", URL: "https://x.example/"})
	issue := func(c auth.Claims) string { return fx.svc.Signer.Issue(c) }
	later := time.Now().Add(time.Hour).Unix()
	base := fx.srv.URL + "/v1/share-targets/corp-share/pages/"
	tickets := map[string]string{
		"expired":      issue(auth.Claims{Subject: "maya", Tenant: "home", ExpiresAtUnix: time.Now().Add(-time.Minute).Unix(), Kind: auth.KindShareTarget, ID: "corp-share", Data: string(card)}),
		"other kind":   issue(auth.Claims{Subject: "maya", Tenant: "home", ExpiresAtUnix: later, Kind: auth.KindSiteFrame, ID: "corp-share", Data: string(card)}),
		"a session":    fx.token,
		"other target": issue(auth.Claims{Subject: "maya", Tenant: "home", ExpiresAtUnix: later, Kind: auth.KindShareTarget, ID: "other-share", Data: string(card)}),
		"garbage":      "not-a-ticket",
	}
	for name, ticket := range tickets {
		for _, p := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodGet, "session"}, {http.MethodPost, "send"}} {
			var body any
			if p.method == http.MethodPost {
				body = wire.ShareSend{DeliveryID: "d", Dest: shareDest(), HostToken: testHostToken}
			}
			if resp, raw := fx.do(t, p.method, base+ticket+"/"+p.suffix, body, ""); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s ticket, %s /%s = %d %s, want 401", name, p.method, p.suffix, resp.StatusCode, raw)
			}
		}
	}
	for name, req := range map[string]wire.ShareSend{
		"no token":      {DeliveryID: "d", Dest: shareDest()},
		"no delivery":   {Dest: shareDest(), HostToken: testHostToken},
		"no dest":       {DeliveryID: "d", HostToken: testHostToken},
		"too many":      {DeliveryID: "d", Dest: make([]wire.ShareDest, wire.MaxShareDest+1), HostToken: testHostToken},
		"bad dest type": {DeliveryID: "d", Dest: []wire.ShareDest{{ID: "x", Type: "channel"}}, HostToken: testHostToken},
	} {
		if raw, code := fx.sendTo(t, page, req); code != http.StatusBadRequest {
			t.Errorf("%s: send = %d %s, want 400", name, code, raw)
		}
	}
	if n := fx.calls(); n != 0 {
		t.Fatalf("the target was called %d times", n)
	}
}

// The share page's calls answer a preflight for its opaque origin, without
// credentials, and the rest of the API still does not.
func TestSharePageAnswersPreflightWithoutCredentials(t *testing.T) {
	fx := newShareFixture(t)
	page, _, _ := fx.startSession(t, "corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"})
	resp, _ := fx.do(t, http.MethodOptions, page+"send", nil, "")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") || resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight = %d %v", resp.StatusCode, resp.Header)
	}
	other, _ := fx.do(t, http.MethodOptions, fx.srv.URL+"/v1/my/share-targets", nil, "")
	if other.Header.Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("a member route answers CORS for any origin")
	}
}

// A target that refuses is a 502 carrying its reason, audited as failed; the
// other app's token is in no response, log line or audit row.
func TestShareTargetNeverLogsHostToken(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prev); log.SetOutput(io.Discard) })

	fx := newShareFixture(t)
	*fx.status, *fx.reason = http.StatusBadGateway, "the bot may not post there"
	page, _, _ := fx.startSession(t, "corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"})
	raw, code := fx.sendTo(t, page, wire.ShareSend{DeliveryID: "d", Dest: shareDest(), HostToken: testHostToken})
	if code != http.StatusBadGateway || !strings.Contains(string(raw), "the bot may not post there") {
		t.Fatalf("send = %d %s", code, raw)
	}
	fx.received(t)
	for where, text := range map[string]string{"response": string(raw), "logs": logs.String(), "audit": func() string { b, _ := json.Marshal(fx.audit.recs); return string(b) }()} {
		if strings.Contains(text, testHostToken) {
			t.Errorf("the host token is in the %s: %s", where, text)
		}
	}
	var failed bool
	for _, rec := range fx.audit.recs {
		failed = failed || (rec.Kind == "share-target" && rec.Action == "share" && rec.Outcome == wire.AuditOutcomeFailed && rec.Detail == wire.ShareItemRoom)
	}
	if !failed {
		t.Fatalf("no failed share audit row in %+v", fx.audit.recs)
	}
}

// A target outside the member's organization scope is not theirs to use, and
// a ticket already issued for it stops working when it is taken off.
func TestShareTargetHiddenOutsideTheTenantScope(t *testing.T) {
	fx := newShareFixture(t)
	page, _, code := fx.startSession(t, "corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"})
	if code != http.StatusOK {
		t.Fatalf("session = %d", code)
	}
	resp, raw := fx.do(t, http.MethodPut, fx.srv.URL+"/v1/platform/components/corp-share/tenants?kind=share-target", map[string]any{"enabledTenants": []string{"someone-else"}}, fx.token)
	if resp.StatusCode >= 300 {
		t.Fatalf("scope the target: %d %s", resp.StatusCode, raw)
	}
	if _, raw, code := fx.startSession(t, "corp-share", wire.ShareItem{Kind: wire.ShareItemRoom, RoomID: "r1"}); code != http.StatusNotFound {
		t.Fatalf("new session = %d %s, want 404", code, raw)
	}
	if presp, _ := fx.do(t, http.MethodGet, page, nil, ""); presp.StatusCode != http.StatusNotFound {
		t.Fatalf("page of an issued ticket = %d, want 404", presp.StatusCode)
	}
	if raw, code := fx.sendTo(t, page, wire.ShareSend{DeliveryID: "d", Dest: shareDest(), HostToken: testHostToken}); code != http.StatusNotFound {
		t.Fatalf("send of an issued ticket = %d %s, want 404", code, raw)
	}
	if n := fx.calls(); n != 0 {
		t.Fatalf("the target was called %d times", n)
	}
}
