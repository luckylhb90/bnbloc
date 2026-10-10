package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

func listMySessions(t *testing.T, srvURL, bearer string) []mySession {
	t.Helper()
	code, raw := authedDo(t, http.MethodGet, srvURL+"/v1/my/sessions", bearer)
	var list struct {
		Sessions []mySession `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &list); code != http.StatusOK || err != nil {
		t.Fatalf("my sessions = %d %s", code, raw)
	}
	return list.Sessions
}

// G4: "sign out all other devices" revokes every other sign-in and the agents
// forked from them, but keeps the calling device and its own agent bridge
// (whose session records the device it was forked from).
func TestRevokeOtherSessionsKeepsThisDeviceAndItsAgent(t *testing.T) {
	_, srv, _ := refreshGateway(t)
	here := loginGrant(t, srv.URL)
	_, hereAgent := refreshWith(t, srv.URL, "/v1/auth/refresh/fork", here.RefreshToken)
	other := loginGrant(t, srv.URL)
	_, otherAgent := refreshWith(t, srv.URL, "/v1/auth/refresh/fork", other.RefreshToken)
	third := loginGrant(t, srv.URL)

	sessions := listMySessions(t, srv.URL, here.Token)
	if len(sessions) != 5 {
		t.Fatalf("sessions = %+v; want 5", sessions)
	}
	var hereID string
	for _, s := range sessions {
		if s.Current {
			hereID = s.ID
		}
	}
	forkedFromHere := 0
	for _, s := range sessions {
		if s.Client == clientBridge && s.ParentID == "" {
			t.Fatalf("forked session without its parent: %+v", s)
		}
		if s.ParentID == hereID {
			forkedFromHere++
		}
	}
	if hereID == "" || forkedFromHere != 1 {
		t.Fatalf("sessions = %+v; want this device current with one agent forked from it", sessions)
	}

	// A bare DELETE never means "revoke everything".
	if code, _ := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions", here.Token); code != http.StatusBadRequest {
		t.Fatalf("DELETE without others=1 = %d, want 400", code)
	}
	code, raw := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions?others=1", here.Token)
	var out struct {
		Revoked int `json:"revoked"`
	}
	_ = json.Unmarshal(raw, &out)
	if code != http.StatusOK || out.Revoked != 3 {
		t.Fatalf("revoke others = %d %s; want 3 revoked", code, raw)
	}
	for name, tok := range map[string]string{"other": other.RefreshToken, "other agent": otherAgent.RefreshToken, "third": third.RefreshToken} {
		if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", tok); c != http.StatusUnauthorized {
			t.Fatalf("%s refresh after revoke-others = %d, want 401", name, c)
		}
	}
	for name, tok := range map[string]string{"this device": here.RefreshToken, "its agent": hereAgent.RefreshToken} {
		if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", tok); c != http.StatusOK {
			t.Fatalf("%s refresh after revoke-others = %d, want 200", name, c)
		}
	}
	if n := len(listMySessions(t, srv.URL, here.Token)); n != 2 {
		t.Fatalf("remaining sessions = %d, want 2", n)
	}
	if code, _ := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions?others=1", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous revoke-others = %d, want 401", code)
	}
}

// G4: revoking a device also revokes the agent bridge forked from it.
func TestRevokeDeviceRevokesItsAgent(t *testing.T) {
	_, srv, _ := refreshGateway(t)
	here := loginGrant(t, srv.URL)
	other := loginGrant(t, srv.URL)
	_, otherAgent := refreshWith(t, srv.URL, "/v1/auth/refresh/fork", other.RefreshToken)
	var otherID string
	for _, s := range listMySessions(t, srv.URL, other.Token) {
		if s.Current {
			otherID = s.ID
		}
	}
	if code, _ := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions/"+otherID, here.Token); code != http.StatusNoContent {
		t.Fatalf("revoke device = %d", code)
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", otherAgent.RefreshToken); c != http.StatusUnauthorized {
		t.Fatalf("agent of a revoked device refresh = %d, want 401", c)
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", here.RefreshToken); c != http.StatusOK {
		t.Fatalf("this device refresh = %d, want 200", c)
	}
}
