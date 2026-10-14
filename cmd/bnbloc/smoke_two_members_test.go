package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The fake gateway's second member: an API token's svc: subject whose
// opencode agent answers the two-members scenario.
const (
	fakeMate      = "svc:mate"
	fakeMateToken = "bnbloc_at_mate"
)

// fakeTwoMembers is the fake gateway's state for the two-members scenario.
type fakeTwoMembers struct {
	mateAgents []myAgent
	members    []string
	note       string // A's plain message
	budget     string // report.md A's agent wrote
	requests   []smokeAccessRequest
	approved   bool

	noAttach  bool // B's agent cannot read A's attachment
	noRequest bool // a refused borrow raises no request
	leak      bool // the borrowed turn sees A's report.md
	named     bool // B's agent names A by display name
}

var (
	codenameRE = regexp.MustCompile(`PROJECT-[0-9a-f]{8}`)
	dateRE     = regexp.MustCompile(`2030-11-\d\d`)
	budgetRE   = regexp.MustCompile(`Budget: (\d+)`)
	priceRE    = regexp.MustCompile(`Unit price: (\d+)`)
)

func (g *fakeSmokeGateway) twoMemberRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/api-token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if g.two == nil || in.Token != fakeMateToken {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": testSession(fakeMate, nil), "subject": fakeMate, "tenant": "t1", "role": "member"})
	})
	mux.HandleFunc("POST /v1/rooms/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.two == nil || bearerSubject(r) != "alice" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		g.two.members = append(g.two.members, in.UserID)
	})
	mux.HandleFunc("GET /v1/borrow/quota-requests", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		out := []smokeAccessRequest{}
		if g.two != nil && r.URL.Query().Get("role") == "owner" && bearerSubject(r) == "alice" {
			for _, q := range g.two.requests {
				if q.Status == r.URL.Query().Get("status") || r.URL.Query().Get("status") == "" {
					out = append(out, q)
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": out})
	})
	mux.HandleFunc("POST /v1/borrow/quota-requests/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		for i, q := range g.two.requests {
			if q.ID == r.PathValue("id") && bearerSubject(r) == "alice" {
				g.two.requests[i].Status = "approved"
				g.two.approved = true
				_ = json.NewEncoder(w).Encode(map[string]any{"authorized": true})
				return
			}
		}
		http.Error(w, "no such request", http.StatusNotFound)
	})
	mux.HandleFunc("GET /v1/users/profiles", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": []map[string]string{{"userId": "alice", "displayName": "Alice Liddell"}}})
	})
}

// answerTwoMembers answers the scenario's turns: A's agent (alice/claude)
// and B's (svc:mate/opencode).
func (g *fakeSmokeGateway) answerTwoMembers(room, from, text string, recipient json.RawMessage, attachmentIDs []string) {
	var rcpt smokeRecipient
	if len(recipient) > 0 {
		_ = json.Unmarshal(recipient, &rcpt)
	}
	g.mu.Lock()
	t := g.two
	var replies [][3]string // owner, provider, text
	switch {
	case len(recipient) == 0 && from == "alice":
		t.note = text
	case rcpt.OwnerID == "alice" && strings.HasPrefix(text, "Create the file report.md"):
		if m := budgetRE.FindStringSubmatch(text); m != nil {
			t.budget = m[1]
		}
		replies = append(replies, [3]string{"alice", "claude", "DONE"})
	case rcpt.OwnerID == "alice":
		replies = append(replies, [3]string{"alice", "claude", "OK"})
	case rcpt.OwnerID == fakeMate && strings.HasPrefix(text, "Answer from this room"):
		price := "unreadable"
		if !t.noAttach {
			for _, content := range g.fakeAgent.files {
				if m := priceRE.FindStringSubmatch(content); m != nil {
					price = m[1]
				}
			}
		}
		who := "alice"
		if t.named {
			who = "Alice Liddell (alice)"
		}
		replies = append(replies, [3]string{fakeMate, "opencode", "codename=" + codenameRE.FindString(t.note) + "\ndate=" + dateRE.FindString(t.note) +
			"\nbudget=" + t.budget + "\nprice=" + price + "\nposted by " + who})
	case rcpt.OwnerID == fakeMate && strings.Contains(text, "'Reply with OK.'"):
		if !t.noRequest {
			t.requests = append(t.requests, smokeAccessRequest{ID: "qr_1", Requester: fakeMate, Kind: "access", Status: "pending"})
		}
		replies = append(replies, [3]string{fakeMate, "opencode", "Failed: alice has not authorized borrowing their claude in this room. They have been asked to approve it."})
	case rcpt.OwnerID == fakeMate && strings.Contains(text, "Run ls -a"):
		if t.approved {
			ls := ".\n..\n.room"
			if t.leak {
				ls += "\nreport.md"
			}
			replies = append(replies, [3]string{"alice", "claude", ls}, [3]string{fakeMate, "opencode", "It answered: " + ls})
		}
	}
	g.mu.Unlock()
	for _, rp := range replies {
		raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"payload": map[string]any{
			"role": "assistant", "text": rp[2], "ownerUserId": rp[0], "provider": rp[1],
		}}}})
		g.mu.Lock()
		g.seq++
		ev := smokeEvent{Sequence: g.seq, Kind: "agent_messages", Payload: raw}
		g.mu.Unlock()
		go func(ev smokeEvent) {
			time.Sleep(10 * time.Millisecond)
			g.publish(room, ev, true)
		}(ev)
	}
}

func newTwoMembersGateway(t *testing.T, set func(*fakeTwoMembers)) *fakeSmokeGateway {
	g := newFakeSmokeGateway(t)
	g.agents = []myAgent{{Core: "acme", Provider: "claude", TargetID: "acme/local:claude", Online: true}}
	g.two = &fakeTwoMembers{mateAgents: []myAgent{{Core: "acme", Provider: "opencode", TargetID: "acme/local:opencode", Online: true}}}
	if set != nil {
		set(g.two)
	}
	return g
}

func twoMembersOptions() smokeOptions {
	opts := smokeTestOptions()
	opts.Agent = "acme/claude"
	opts.Scenario = scenarioTwoMembers
	opts.TeammateToken = fakeMateToken
	opts.TeammateAgent = "opencode"
	opts.PollEvery = 10 * time.Millisecond
	opts.Settle = 80 * time.Millisecond
	return opts
}

func TestSmokeTwoMembersPasses(t *testing.T) {
	g := newTwoMembersGateway(t, func(m *fakeTwoMembers) { m.named = true })
	out, err := runFakeSmoke(t, g, twoMembersOptions())
	if err != nil {
		t.Fatalf("scenario failed: %v\n%s", err, out)
	}
	for _, want := range []string{"PASS agent (", "PASS teammate (", "PASS A's setup (", "PASS others' messages (", "PASS others' deliverables (",
		"PASS others' attachments (", `INFO names: A named "Alice Liddell"`, "PASS borrow request (", "PASS approve (", "PASS borrowed run (",
		"PASS borrower isolation (", "PASS delete room ("} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if len(g.two.members) != 1 || g.two.members[0] != fakeMate || !g.two.approved {
		t.Fatalf("members %v approved %v", g.two.members, g.two.approved)
	}
}

// Each broken property fails on its own line; a failed run keeps its room.
func TestSmokeTwoMembersNamesWhatBroke(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeTwoMembers)
		fail string
		pass string
	}{
		{"attachment unread", func(m *fakeTwoMembers) { m.noAttach = true }, "FAIL others' attachments: B's agent did not read pricing.md", "PASS others' deliverables ("},
		{"no borrow request", func(m *fakeTwoMembers) { m.noRequest = true }, "FAIL borrow request: A holds no pending access request", "PASS others' attachments ("},
		{"borrower sees the owner's files", func(m *fakeTwoMembers) { m.leak = true }, "FAIL borrower isolation: the borrowed turn's workspace holds A's report.md", "PASS borrowed run ("},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newTwoMembersGateway(t, tc.set)
			opts := twoMembersOptions()
			opts.AgentTimeout = 400 * time.Millisecond
			opts.StepTimeout = 300 * time.Millisecond
			out, err := runFakeSmoke(t, g, opts)
			if !errors.Is(err, errSmokeFailed) {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.fail) || !strings.Contains(out, tc.pass) {
				t.Fatalf("want %q and %q:\n%s", tc.fail, tc.pass, out)
			}
			if len(g.deleted) != 0 || !strings.Contains(out, "SKIP delete room (kept: the scenario failed)") {
				t.Fatalf("a failed scenario's room was not kept: deleted=%v\n%s", g.deleted, out)
			}
		})
	}
}

// A teammate token the gateway refuses fails the teammate step.
func TestSmokeTwoMembersBadToken(t *testing.T) {
	g := newTwoMembersGateway(t, nil)
	opts := twoMembersOptions()
	opts.TeammateToken = "bnbloc_at_wrong"
	out, err := runFakeSmoke(t, g, opts)
	if !errors.Is(err, errSmokeFailed) || !strings.Contains(out, "FAIL teammate: teammate token:") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestTeammateTokenFrom(t *testing.T) {
	env := func(k string) string {
		if k == teammateTokenEnv {
			return " from-env "
		}
		return ""
	}
	if got := teammateTokenFrom("", env); got != "from-env" {
		t.Fatalf("env token = %q", got)
	}
	if got := teammateTokenFrom("flag", env); got != "flag" {
		t.Fatalf("flag token = %q", got)
	}
}
