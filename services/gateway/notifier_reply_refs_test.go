package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// registerRef signs and posts a reply reference as component name.
func (f *replyFixture) registerRef(t *testing.T, name, secret string, req wire.NotifierReplyRefRequest) (int, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, f.srv.URL+wire.NotifierReplyRefsPath, bytes.NewReader(body))
	r.Header.Set(wire.NotifierComponentHeader, name)
	r.Header.Set(wire.NotificationSignatureHeader, notify.Sign(secret, time.Now(), body))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A notifier registers (message ref, recipient account) → token, and any
// replica later posts the reply with {ref, sender} only: bnbloc resolves the
// token, posts as the token's member with the token's addressing, keeps the
// idempotency, and refuses a sender the message was not sent to.
func TestNotifierReplyByRegisteredRef(t *testing.T) {
	f := newReplyFixture(t)
	tok := f.mint(t, "corp", &wire.NotificationReply{Recipient: &wire.NotificationReplyRecipient{OwnerID: "carol", Provider: "codex", Core: "local"}})
	reg := wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: "v1:1:+900", Recipient: "+11"}
	if status, raw := f.registerRef(t, "corp", f.secret, reg); status != http.StatusNoContent {
		t.Fatalf("register = %d %s", status, raw)
	}
	// Re-registering the same binding (a notifier retry) is a no-op.
	if status, raw := f.registerRef(t, "corp", f.secret, reg); status != http.StatusNoContent {
		t.Fatalf("re-register = %d %s", status, raw)
	}

	status, out, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{Ref: "v1:1:+900", Sender: "+11", Text: "add tests", IdempotencyKey: "v1:5:+11"})
	if status != http.StatusOK || out.RoomID != "room-1" || out.Sequence == 0 {
		t.Fatalf("reply by ref = %d %s", status, raw)
	}
	status, again, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{Ref: "v1:1:+900", Sender: "+11", Text: "add tests", IdempotencyKey: "v1:5:+11"})
	if status != http.StatusOK || again.Sequence != out.Sequence {
		t.Fatalf("retried reply by ref = %d %s (first %d)", status, raw, out.Sequence)
	}
	msgs := f.feed(t)
	if len(msgs) != 1 || msgs[0].Author != "alice" || msgs[0].ClientMsgID != notify.ReplyClientMsgID("corp", "alice", "v1:5:+11") {
		t.Fatalf("feed = %+v", msgs)
	}
	if len(f.jobs.jobs) != 1 || f.jobs.jobs[0].OwnerUserID != "carol" || f.jobs.jobs[0].RequesterUserID != "alice" {
		t.Fatalf("agent jobs = %+v", f.jobs.jobs)
	}

	// The recipient check is bnbloc's: another account quoting the same
	// message (a forward) resolves to nothing.
	if status, _, raw := f.reply(t, "corp", f.secret, wire.NotifierReplyRequest{Ref: "v1:1:+900", Sender: "+22", Text: "me too", IdempotencyKey: "v1:6:+22"}); status != http.StatusNotFound {
		t.Fatalf("foreign sender = %d %s", status, raw)
	}
	// A reference is bound to the notifier that registered it.
	other := f.mint(t, "other-notifier", &wire.NotificationReply{})
	if status, raw := f.registerRef(t, "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: other, Ref: "v1:2:+900", Recipient: "+11"}); status != http.StatusNotFound {
		t.Fatalf("register a foreign token = %d %s", status, raw)
	}
	if len(f.feed(t)) != 1 {
		t.Fatal("a refused reply was posted")
	}
}

func TestNotifierReplyRefRejections(t *testing.T) {
	f := newReplyFixture(t)
	tok := f.mint(t, "corp", &wire.NotificationReply{})
	long := strings.Repeat("r", notify.MaxReplyRefLen+1)
	regCases := []struct {
		name   string
		comp   string
		secret string
		req    wire.NotifierReplyRefRequest
		want   int
	}{
		{"bad signature", "corp", "whsec_wrong", wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: "r1", Recipient: "+11"}, http.StatusUnauthorized},
		{"unknown component", "ghost", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: "r1", Recipient: "+11"}, http.StatusForbidden},
		{"unknown token", "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok + "x", Ref: "r1", Recipient: "+11"}, http.StatusNotFound},
		{"missing ref", "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok, Recipient: "+11"}, http.StatusBadRequest},
		{"missing recipient", "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: "r1"}, http.StatusBadRequest},
		{"ref too long", "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: long, Recipient: "+11"}, http.StatusBadRequest},
	}
	for _, c := range regCases {
		if status, raw := f.registerRef(t, c.comp, c.secret, c.req); status != c.want {
			t.Errorf("register %s = %d %s, want %d", c.name, status, raw, c.want)
		}
	}
	// A reference never moves to another token.
	if status, raw := f.registerRef(t, "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok, Ref: "r1", Recipient: "+11"}); status != http.StatusNoContent {
		t.Fatalf("register = %d %s", status, raw)
	}
	tok2 := f.mint(t, "corp", &wire.NotificationReply{})
	if status, raw := f.registerRef(t, "corp", f.secret, wire.NotifierReplyRefRequest{ReplyToken: tok2, Ref: "r1", Recipient: "+11"}); status != http.StatusConflict {
		t.Fatalf("rebind = %d %s", status, raw)
	}

	replyCases := []struct {
		name string
		req  wire.NotifierReplyRequest
		want int
	}{
		{"token and ref", wire.NotifierReplyRequest{ReplyToken: tok, Ref: "r1", Sender: "+11", Text: "hi", IdempotencyKey: "k"}, http.StatusBadRequest},
		{"ref without sender", wire.NotifierReplyRequest{Ref: "r1", Text: "hi", IdempotencyKey: "k"}, http.StatusBadRequest},
		{"sender without ref", wire.NotifierReplyRequest{Sender: "+11", Text: "hi", IdempotencyKey: "k"}, http.StatusBadRequest},
		{"neither", wire.NotifierReplyRequest{Text: "hi", IdempotencyKey: "k"}, http.StatusBadRequest},
		{"unknown ref", wire.NotifierReplyRequest{Ref: "r2", Sender: "+11", Text: "hi", IdempotencyKey: "k"}, http.StatusNotFound},
	}
	for _, c := range replyCases {
		if status, _, raw := f.reply(t, "corp", f.secret, c.req); status != c.want {
			t.Errorf("reply %s = %d %s, want %d", c.name, status, raw, c.want)
		}
	}

	// The reference expires with its token.
	f.svc.Now = func() time.Time { return time.Now().Add(notify.ReplyTokenTTL + time.Minute) }
	defer func() { f.svc.Now = time.Now }()
	body, _ := json.Marshal(wire.NotifierReplyRequest{Ref: "r1", Sender: "+11", Text: "late", IdempotencyKey: "k9"})
	r, _ := http.NewRequest(http.MethodPost, f.srv.URL+wire.NotifierRepliesPath, bytes.NewReader(body))
	r.Header.Set(wire.NotifierComponentHeader, "corp")
	r.Header.Set(wire.NotificationSignatureHeader, notify.Sign(f.secret, f.svc.Now(), body))
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != http.StatusGone {
		t.Fatalf("expired reference = %v %v", resp, err)
	}
	f.svc.Now = time.Now

	// Uninstalling the notifier deletes its references with its tokens.
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/platform/components/corp?kind=notifier", nil)
	req.Header.Set("Authorization", "Bearer "+f.admin)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("uninstall = %v %v", resp, err)
	}
	if _, ok, _ := f.svc.NotifierReplies.GetNotificationReplyRef(context.Background(), "corp", "r1", "+11"); ok {
		t.Fatal("reference survived uninstall")
	}
	if len(f.feed(t)) != 0 {
		t.Fatal("a refused reply was posted")
	}
}
