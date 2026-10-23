package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/luckylhb90/bnbloc/component"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// replyMembers is a mutable room membership (revocable mid-test).
type replyMembers struct {
	mu      sync.Mutex
	members map[string]bool // room/user
}

func (m *replyMembers) Lookup(roomID, userID string) (membership.Member, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return membership.Member{Role: store.RoleMember}, m.members[roomID+"/"+userID]
}

func (m *replyMembers) Fresh(roomID, userID string) (membership.Member, bool) {
	return m.Lookup(roomID, userID)
}

func (*replyMembers) Forget(string, string) {}

// Preview: this fake has no shared rooms.
func (*replyMembers) Preview(string, string) (string, bool) { return "", false }

func (m *replyMembers) set(roomID, userID string, member bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[roomID+"/"+userID] = member
}

// replyJobs captures the agent jobs realtime's chat path creates.
type replyJobs struct {
	mu   sync.Mutex
	jobs []store.AgentJob
}

func (j *replyJobs) PutAgentJob(_ context.Context, job store.AgentJob) (store.AgentJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jobs = append(j.jobs, job)
	return job, nil
}

type replyFixture struct {
	svc     *Service
	srv     *httptest.Server
	admin   string
	secret  string
	tokens  *notify.ReplyTokens
	members *replyMembers
	jobs    *replyJobs
	chat    *store.MemoryChat
}

// newReplyFixture: a gateway with an installed notifier "corp" (signing
// secret known), a real realtime chat endpoint behind it, and alice a member
// of room-1.
func newReplyFixture(t *testing.T) *replyFixture {
	t.Helper()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifier/discover" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp", "contract": pkgmeta.NotifierContractV1})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(receiver.Close)
	svc, srv, admin, key := signedPackageGatewayWithArtifactStore(t, true, false)
	body := notifierPackageBody(t, "corp", receiver.URL+"/notifier")
	raw, status := postComponent(t, srv.URL, admin, body, signPackage(key.private, body))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if status != http.StatusOK || installed.Secret == "" {
		t.Fatalf("install = %d %s", status, raw)
	}
	f := &replyFixture{
		svc: svc, srv: srv, admin: admin, secret: installed.Secret,
		members: &replyMembers{members: map[string]bool{"room-1/alice": true}},
		jobs:    &replyJobs{}, chat: store.NewMemoryChat(),
	}
	rt := &realtimehttp.Service{Hub: realtime.NewHub(64, 16), Membership: f.members, Chat: f.chat, BridgeJobs: f.jobs}
	rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), rt.Handler()))
	t.Cleanup(rtSrv.Close)
	svc.Backends.Realtime = rtSrv.URL
	// alice is a member of tenant home (the reply checks her org role).
	svc.TenantMemberRole = func(tenantID, userID string) (string, bool) {
		return string(rbac.RoleMember), tenantID == "home" && userID == "alice"
	}
	replies := store.NewMemoryNotificationReplies()
	svc.NotifierReplies = replies
	f.tokens = &notify.ReplyTokens{Store: replies, Now: svc.nowTime}
	return f
}

// mint issues alice's reply token for a notification in room-1.
func (f *replyFixture) mint(t *testing.T, notifier string, reply *wire.NotificationReply) string {
	t.Helper()
	tok, err := f.tokens.Mint(context.Background(), notifier, wire.NotificationPayload{
		ID: "ntf_1", TenantID: "home", RoomID: "room-1", Reply: reply,
	}, "alice")
	if err != nil || tok == "" {
		t.Fatalf("mint = %q %v", tok, err)
	}
	return tok
}

// reply posts a signed reply as component name with secret.
func (f *replyFixture) reply(t *testing.T, name, secret string, req wire.NotifierReplyRequest) (int, wire.NotifierReplyResponse, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, f.srv.URL+wire.NotifierRepliesPath, bytes.NewReader(body))
	if name != "" {
		r.Header.Set(component.ComponentHeader, name)
	}
	r.Header.Set(wire.NotificationSignatureHeader, notify.Sign(secret, time.Now(), body))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out wire.NotifierReplyResponse
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, string(raw)
}

func (f *replyFixture) feed(t *testing.T) []store.RoomMessage {
	t.Helper()
	msgs, err := f.chat.RoomFeed(tenant.NewContext(context.Background(), "home"), "room-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// A mention reply is human chat from the member; an agent-result reply is
// addressed to the same agent (realtime creates the agent job, the bridge
// then enforces borrow rules); a retried idempotency key posts once.
func TestNotifierReplyPostsAsMemberWithAddressing(t *testing.T) {
	f := newReplyFixture(t)
	roomTok := f.mint(t, "corp", &wire.NotificationReply{})
	status, out, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: roomTok, Text: " on it ", IdempotencyKey: "msg-1"})
	if status != http.StatusOK || out.RoomID != "room-1" || out.Sequence == 0 {
		t.Fatalf("room reply = %d %s", status, raw)
	}
	// The same messenger message again (a retry) returns the same sequence.
	status, again, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: roomTok, Text: "on it", IdempotencyKey: "msg-1"})
	if status != http.StatusOK || again.Sequence != out.Sequence {
		t.Fatalf("retry = %d %s (first seq %d)", status, raw, out.Sequence)
	}
	msgs := f.feed(t)
	if len(msgs) != 1 || msgs[0].Author != "alice" || msgs[0].ClientMsgID != notify.ReplyClientMsgID("corp", "alice", "msg-1") {
		t.Fatalf("feed = %+v", msgs)
	}
	var payload map[string]any
	_ = json.Unmarshal(msgs[0].Payload, &payload)
	if payload["text"] != "on it" || payload["userId"] != "alice" || payload["recipient"] != nil {
		t.Fatalf("room reply payload = %v", payload)
	}
	if len(f.jobs.jobs) != 0 {
		t.Fatalf("a room reply drove an agent: %+v", f.jobs.jobs)
	}

	agentTok := f.mint(t, "corp", &wire.NotificationReply{Recipient: &wire.NotificationReplyRecipient{OwnerID: "carol", Provider: "codex", Core: "local"}})
	status, out2, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: agentTok, Text: "now add tests", IdempotencyKey: "msg-2"})
	if status != http.StatusOK || out2.Sequence <= out.Sequence {
		t.Fatalf("agent reply = %d %s", status, raw)
	}
	if len(f.jobs.jobs) != 1 {
		t.Fatalf("agent jobs = %+v", f.jobs.jobs)
	}
	if j := f.jobs.jobs[0]; j.RequesterUserID != "alice" || j.OwnerUserID != "carol" || j.Provider != "codex" || j.Core != "local" || j.MessageText != "now add tests" {
		t.Fatalf("agent job = %+v", j)
	}
	// The token stays usable for further replies until it expires.
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: agentTok, Text: "and docs", IdempotencyKey: "msg-3"}); status != http.StatusOK {
		t.Fatalf("second reply on a token = %d %s", status, raw)
	}
}

func TestNotifierReplyRejections(t *testing.T) {
	f := newReplyFixture(t)
	tok := f.mint(t, "corp", &wire.NotificationReply{})
	ok := wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi", IdempotencyKey: "k"}
	cases := []struct {
		name   string
		comp   string
		secret string
		req    wire.NotifierReplyRequest
		want   int
	}{
		{"no component header", "", f.secret, ok, http.StatusUnauthorized},
		{"bad signature", "corp", "whsec_wrong", ok, http.StatusUnauthorized},
		{"unknown component", "ghost", f.secret, ok, http.StatusForbidden},
		{"unknown token", "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok + "x", Text: "hi", IdempotencyKey: "k"}, http.StatusNotFound},
		{"missing idempotency key", "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi"}, http.StatusBadRequest},
		{"empty text", "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "  ", IdempotencyKey: "k"}, http.StatusBadRequest},
		{"text over 16KB", "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: strings.Repeat("x", 16*1024+1), IdempotencyKey: "k"}, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		if status, _, raw := f.reply(t, c.comp, c.secret, c.req); status != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, status, raw, c.want)
		}
	}
	// A stale signature timestamp is refused (replay window).
	body, _ := json.Marshal(ok)
	r, _ := http.NewRequest(http.MethodPost, f.srv.URL+wire.NotifierRepliesPath, bytes.NewReader(body))
	r.Header.Set(component.ComponentHeader, "corp")
	r.Header.Set(wire.NotificationSignatureHeader, notify.Sign(f.secret, time.Now().Add(-10*time.Minute), body))
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale signature = %v %v", resp, err)
	}
	// A token is bound to the notifier it was delivered through.
	other := f.mint(t, "other-notifier", &wire.NotificationReply{})
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: other, Text: "hi", IdempotencyKey: "k"}); status != http.StatusNotFound {
		t.Fatalf("foreign token = %d %s", status, raw)
	}
	if len(f.feed(t)) != 0 {
		t.Fatal("a rejected reply was posted")
	}
}

func TestNotifierReplyExpiryMembershipAndRollout(t *testing.T) {
	f := newReplyFixture(t)
	tok := f.mint(t, "corp", &wire.NotificationReply{})

	// Membership is checked when the reply is posted, not when minted.
	f.members.set("room-1", "alice", false)
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi", IdempotencyKey: "k1"}); status != http.StatusForbidden {
		t.Fatalf("revoked member = %d %s", status, raw)
	}
	f.members.set("room-1", "alice", true)

	// Rollout off: the notifier may not post for anyone.
	if status := doJSON(t, f.srv, http.MethodPut, "/v1/platform/components/corp/rollout?kind=notifier", f.admin, `{"rollout":"off"}`); status != http.StatusNoContent {
		t.Fatalf("rollout off = %d", status)
	}
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi", IdempotencyKey: "k2"}); status != http.StatusForbidden {
		t.Fatalf("rollout off reply = %d %s", status, raw)
	}
	if status := doJSON(t, f.srv, http.MethodPut, "/v1/platform/components/corp/rollout?kind=notifier", f.admin, `{"rollout":"auto"}`); status != http.StatusNoContent {
		t.Fatalf("rollout auto = %d", status)
	}
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi", IdempotencyKey: "k3"}); status != http.StatusOK {
		t.Fatalf("reply after rollout auto = %d %s", status, raw)
	}

	// Expired: 7 days later the token is gone.
	f.svc.Now = func() time.Time { return time.Now().Add(notify.ReplyTokenTTL + time.Minute) }
	f.tokens.Now = f.svc.Now
	expiredAt := f.svc.Now()
	// The signature is still checked against the real clock window: sign
	// with the same skewed clock the gateway now runs on.
	body, _ := json.Marshal(wire.NotifierReplyRequest{ReplyToken: tok, Text: "late", IdempotencyKey: "k4"})
	r, _ := http.NewRequest(http.MethodPost, f.srv.URL+wire.NotifierRepliesPath, bytes.NewReader(body))
	r.Header.Set(component.ComponentHeader, "corp")
	r.Header.Set(wire.NotificationSignatureHeader, notify.Sign(f.secret, expiredAt, body))
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != http.StatusGone {
		t.Fatalf("expired token = %v %v", resp, err)
	}
	f.svc.Now = time.Now

	// Uninstalling the notifier deletes its tokens.
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/platform/components/corp?kind=notifier", nil)
	req.Header.Set("Authorization", "Bearer "+f.admin)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("uninstall = %v %v", resp, err)
	}
	if _, err := f.tokens.Resolve(context.Background(), tok); err != notify.ErrReplyTokenUnknown {
		t.Fatalf("token after uninstall: %v", err)
	}
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{ReplyToken: tok, Text: "hi", IdempotencyKey: "k5"}); status != http.StatusForbidden {
		t.Fatalf("uninstalled notifier = %d %s", status, raw)
	}
}
