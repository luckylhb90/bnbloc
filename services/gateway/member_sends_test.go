package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A send from a member's computer: a POST page call carries the member's
// grant (a GET none); the service asks with it, signed; it is queued only
// while one of the member's own bridges is live, once per grant, a GET or
// HEAD of https only, audited; the member's bridge takes it with its claim
// and its egress call reaches the service's send path with the send
// attested and the queued request, never what the bridge put in; another
// member's bridge cannot.
func TestSendFromAMembersComputer(t *testing.T) {
	fx := newServiceFixture(t)
	_, raw := fx.do("maya", http.MethodPost, "/v1/my/component-pages/conn/connections", nil, nil)
	var sess wire.ComponentPageSession
	_ = json.Unmarshal(raw, &sess)
	pagePath := strings.TrimPrefix(sess.URL, fx.srv.URL)
	memberOf := func() component.Member {
		var m component.Member
		_ = component.DecodeHeader(fx.last(t).identity, &m)
		return m
	}
	_, _ = fx.do("", http.MethodGet, pagePath, nil, nil)
	if memberOf().Act != "" {
		t.Fatal("a page view carries a grant")
	}
	_, _ = fx.do("", http.MethodPost, pagePath+"try", []byte("name=okx"), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	act := memberOf().Act
	if act == "" {
		t.Fatal("a page action carries no grant")
	}

	client := &component.ServiceClient{BaseURL: fx.srv.URL, Name: "conn", Secret: fx.secret}
	ctx := context.Background()
	if _, err := client.SendFromMember(ctx, act, "GET", "https://www.okx.com/api/v5/account/balance"); !errors.Is(err, component.ErrNoMemberComputer) {
		t.Fatalf("no bridge = %v", err)
	}
	if err := fx.jobs.PutBridgeHeartbeat(tenant.NewContext(ctx, "home"), store.BridgeHeartbeat{BridgeID: "br-maya", OwnerUserID: "maya", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"POST", "https://www.okx.com/x"}, {"GET", "http://www.okx.com/x"}, {"GET", "https://u:p@www.okx.com/x"}} {
		if _, err := client.SendFromMember(ctx, act, bad[0], bad[1]); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("%v = %v", bad, err)
		}
	}
	if _, err := client.SendFromMember(ctx, "forged", "GET", "https://www.okx.com/x"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a forged grant = %v", err)
	}
	id, err := client.SendFromMember(ctx, act, "GET", "https://www.okx.com/api/v5/account/balance")
	if err != nil || id == "" {
		t.Fatalf("send = %q %v", id, err)
	}
	if _, err := client.SendFromMember(ctx, act, "GET", "https://www.okx.com/api/v5/account/balance"); err == nil || !strings.Contains(err.Error(), "used") {
		t.Fatalf("a grant twice = %v", err)
	}
	found := false
	for _, a := range fx.audit.recs {
		found = found || (a.Name == "conn" && a.Action == "send" && a.Actor == "maya" && strings.Contains(a.Detail, "GET www.okx.com/api/v5/account/balance"))
	}
	if !found {
		t.Fatalf("audit = %+v", fx.audit.recs)
	}

	if resp, _ := fx.do("maya", http.MethodPost, component.SendProxyPath+id, []byte(`{"method":"GET","url":"https://evil.example.com/"}`), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an untaken send = %d", resp.StatusCode)
	}
	taken, _ := fx.jobs.TakeBridgeSends(ctx, "maya", "br-maya", time.Now().UnixMilli())
	if len(taken) != 1 || taken[0].SendID != id {
		t.Fatalf("taken = %+v", taken)
	}
	if resp, _ := fx.do("jack", http.MethodPost, component.SendProxyPath+id, []byte(`{}`), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another member's bridge = %d", resp.StatusCode)
	}
	resp, raw := fx.do("maya", http.MethodPost, component.SendProxyPath+id, []byte(`{"method":"DELETE","url":"https://evil.example.com/","body":"eA=="}`), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "204") {
		t.Fatalf("send proxy = %d %s", resp.StatusCode, raw)
	}
	call := fx.last(t)
	var send component.Send
	var in component.EgressRequest
	_ = component.DecodeHeader(call.identity, &send)
	_ = json.Unmarshal(call.body, &in)
	if !call.signed || call.path != component.ServiceSendEgressPath || send.ID != id || send.Member != "maya" || send.Org != "home" ||
		in.Method != "GET" || in.URL != "https://www.okx.com/api/v5/account/balance" || len(in.Body) != 0 {
		t.Fatalf("call = %+v send = %+v in = %+v", call, send, in)
	}
}

// The store's send bounds are the contract's.
func TestSendBoundsAreTheContracts(t *testing.T) {
	if store.BridgeSendWaitMs != component.MaxSendWait.Milliseconds() || store.BridgeSendHeldMs != component.MaxSendHeld.Milliseconds() {
		t.Fatal("store.BridgeSend*Ms differ from component.MaxSend*")
	}
}
