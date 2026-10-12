package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// An attachment URL carries a ticket for that one attachment, never the
// session (web H10): the ticket reads exactly the attachment it names, as
// its subject, while the subject is still a room member; it is no session
// anywhere else.
func TestAttachmentTicketReadsOneAttachment(t *testing.T) {
	var gotPath, gotUser string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUser = r.URL.Path, r.Header.Get("X-Bnbloc-User")
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Backends.Realtime = backend.URL
	var member atomic.Bool
	gw.Rooms = func(string, string) []string {
		if member.Load() {
			return []string{"room-1"}
		}
		return nil
	}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	session := gw.Signer.Issue(auth.Claims{Subject: "alice", Rooms: []string{"room-1"}, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})

	mint := func(path, bearer string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			URL         string `json:"url"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out.URL
	}
	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if resp, _ := mint("/v1/rooms/room-1/attachments/att-1/ticket", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mint without a session: %d, want 401", resp.StatusCode)
	}
	if resp, _ := mint("/v1/rooms/room-2/attachments/att-1/ticket", session); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mint for a room out of reach: %d, want 403", resp.StatusCode)
	}
	resp, link := mint("/v1/rooms/room-1/attachments/att-1/ticket", session)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("mint: %d %v", resp.StatusCode, resp.Header)
	}
	u, err := url.Parse(link)
	if err != nil || u.Path != "/v1/rooms/room-1/attachments/att-1" || u.Query().Get("ticket") == "" || strings.Contains(link, session) {
		t.Fatalf("ticket url = %q", link)
	}
	ticket := u.Query().Get("ticket")

	// Membership is re-checked on every read: a member no longer in the room
	// is refused even with a live ticket.
	if c := get(link); c != http.StatusForbidden {
		t.Fatalf("read after leaving the room: %d, want 403", c)
	}
	member.Store(true)
	if c := get(link); c != http.StatusOK || gotPath != "/v1/rooms/room-1/attachments/att-1" || gotUser != "alice" {
		t.Fatalf("read: %d %s as %q", c, gotPath, gotUser)
	}
	q := "?ticket=" + url.QueryEscape(ticket)
	for _, other := range []string{
		"/v1/rooms/room-1/attachments/att-2",              // another attachment
		"/v1/rooms/room-1/members",                        // not an attachment read
		"/v1/rooms/room-1/artifacts/a/versions/1/content", // a deliverable
	} {
		if c := get(other + q); c != http.StatusUnauthorized {
			t.Errorf("ticket on %s: %d, want 401", other, c)
		}
	}
	// A session is not a ticket.
	if c := get("/v1/rooms/room-1/attachments/att-1?ticket=" + url.QueryEscape(session)); c != http.StatusUnauthorized {
		t.Errorf("session as ticket: %d, want 401", c)
	}
	// A ticket is not a session.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/attachments/att-1", nil)
	req.Header.Set("Authorization", "Bearer "+ticket)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ticket as bearer: %v %v, want 401", resp.StatusCode, err)
	}
}
