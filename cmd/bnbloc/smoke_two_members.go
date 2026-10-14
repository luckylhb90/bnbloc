package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The two-members scenario (`bnbloc smoke --agent A --scenario two-members
// --teammate-token T --teammate-agent P`) checks a room where two people
// each drive their own agent (docs/prd/2026-10-14-multi-member-rooms.md):
// the signed-in member (A) and a teammate (B, the subject of an API token,
// typically a svc: subject whose agent a bridge serves for the check). Each
// property is its own line:
//
//	teammate            B's session (the token exchanged), B added to the
//	                    room, B's agent online
//	others' messages    B's agent knows a codeword and date A posted
//	others' deliverables B's agent reads the budget in report.md, which A's
//	                    agent wrote
//	others' attachments B's agent reads the price in pricing.md, which A
//	                    uploaded
//	names               INFO: whether B's agent named A by display name
//	borrow request      B's agent asks A's agent (ask_agent) unauthorized:
//	                    A holds a pending access request from B
//	borrowed run        once A approves it, A's agent answers B's request
//	borrower isolation  that turn ran in a workspace of B's own: A's
//	                    report.md is not in it
//
// A failed scenario keeps its room for debugging.

// scenarioTwoMembers names the two-members scenario.
const scenarioTwoMembers = "two-members"

// borrowNoticeRE finds whether B's agent passed on the refusal's request.
var borrowNoticeRE = regexp.MustCompile(`(?i)approv|request`)

// teammateTokenEnv carries the teammate's API token (kept out of argv).
const teammateTokenEnv = "BNBLOC_TEAMMATE_API_TOKEN"

// twoMemberFacts are the random facts A plants for B's agent to find.
type twoMemberFacts struct {
	Codename, Date string // A's message
	Budget         string // report.md (A's agent)
	Price          string // pricing.md (A's upload)
}

func newTwoMemberFacts() (twoMemberFacts, error) {
	cw, _, err := scenarioCodeword()
	if err != nil {
		return twoMemberFacts{}, err
	}
	num := func(lo, span int64) (string, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(span))
		if err != nil {
			return "", err
		}
		return fmt.Sprint(lo + n.Int64()), nil
	}
	day, err := num(10, 18)
	if err != nil {
		return twoMemberFacts{}, err
	}
	budget, err := num(1000, 9000)
	if err != nil {
		return twoMemberFacts{}, err
	}
	price, err := num(100, 900)
	if err != nil {
		return twoMemberFacts{}, err
	}
	return twoMemberFacts{Codename: "PROJECT-" + strings.TrimPrefix(cw, "CW-"), Date: "2030-11-" + day, Budget: budget, Price: price}, nil
}

// smokeTwoMembers runs the scenario's checks on the open room (A owns it).
func smokeTwoMembers(ctx context.Context, c *client, w *smokeWaiter, roomID, subject string, opts smokeOptions, run *smokeRun) {
	var aAgent smokeRecipient
	if !run.step("agent", func() (string, error) {
		var err error
		aAgent, err = smokeResolveAgent(ctx, c, subject, opts)
		return aAgent.OwnerID + "/" + aAgent.Provider, err
	}) {
		return
	}
	b := newClient(config{Gateway: c.cfg.Gateway, APIToken: opts.TeammateToken})
	var bAgent smokeRecipient
	if !run.step("teammate", func() (string, error) {
		id, err := b.identity()
		if err != nil {
			return "", fmt.Errorf("teammate token: %w", err)
		}
		if err := c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/members",
			map[string]string{"userId": id.Subject, "role": "member"}, nil); err != nil {
			return "", fmt.Errorf("add %s to the room: %w", id.Subject, err)
		}
		bAgent, err = smokeTeammateAgent(ctx, b, id.Subject, opts)
		return id.Subject + "/" + bAgent.Provider, err
	}) {
		return
	}
	facts, err := newTwoMemberFacts()
	if err != nil {
		run.step("facts", func() (string, error) { return "", err })
		return
	}
	aTurn := func(text string, atts []string, want ...string) (string, error) {
		return smokeScenarioTurn(ctx, c, w, roomID, aAgent, text, atts, opts, want...)
	}
	bTurn := func(text string, want ...string) (string, error) {
		return smokeScenarioTurn(ctx, b, w, roomID, bAgent, text, nil, opts, want...)
	}

	// A plants the three facts: a plain message, a deliverable of A's
	// agent, an attachment A uploads.
	if !run.step("A's setup", func() (string, error) {
		if err := c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", map[string]any{
			"text": "Team note: our project codename is " + facts.Codename + " and it launches on " + facts.Date + ".", "atUnixMs": opts.Now().UnixMilli(),
		}, nil); err != nil {
			return "", err
		}
		if _, err := aTurn("Create the file report.md in your workspace root with exactly two lines: 'Owner: the requester' and 'Budget: "+facts.Budget+"'. Then reply DONE.", nil, "DONE"); err != nil {
			return "", fmt.Errorf("report.md: %w", err)
		}
		att, err := smokeUploadAttachment(ctx, c, roomID, "pricing.md", "text/markdown", []byte("# Pricing\n\nUnit price: "+facts.Price+" per seat\n"), opts.StepTimeout)
		if err != nil {
			return "", err
		}
		_, err = aTurn("Here is the pricing attachment for later. Reply only OK.", []string{att.ID}, "OK")
		return "message, report.md, pricing.md", err
	}) {
		return
	}

	reply, replyErr := bTurn("Answer from this room's conversation, deliverables and attachments; if you cannot read something, say so — never guess. "+
		"Reply exactly as four lines: codename=<value>, date=<value>, budget=<value from report.md>, price=<value from pricing.md>. "+
		"Also name the person who posted the codename.", facts.Codename, facts.Budget, facts.Price)
	if replyErr != nil && reply != "" && strings.HasPrefix(replyErr.Error(), "the reply does not hold") {
		replyErr = nil // B's agent answered: the lines below say what it lacked
	}
	check := func(name, what string, want ...string) {
		run.step(name, func() (string, error) {
			if replyErr != nil {
				return "", replyErr
			}
			for _, s := range want {
				if !strings.Contains(reply, s) {
					return "", fmt.Errorf("B's agent did not read %s (%s): %q", what, s, truncateRunes(reply, 160))
				}
			}
			return strings.Join(want, " "), nil
		})
	}
	check("others' messages", "A's message", facts.Codename, facts.Date)
	check("others' deliverables", "report.md of A's agent", facts.Budget)
	check("others' attachments", "pricing.md A uploaded", facts.Price)
	fmt.Fprintf(run.out, "INFO names: %s\n", smokeNamesNote(ctx, c, subject, reply, opts))

	// Borrowing A's agent: refused until A approves the request it raised.
	askA := "Use your room tool ask_agent to send this task to the agent " + aAgent.OwnerID + "/" + aAgent.Provider + ": "
	refusal, refusalErr := bTurn(askA + "'Reply with OK.' Then tell me its answer, or quote the failure reason word for word.")
	var request smokeAccessRequest
	if !run.step("borrow request", func() (string, error) {
		if refusalErr != nil && refusal == "" {
			return "", refusalErr
		}
		var err error
		request, err = smokePendingAccessRequest(ctx, c, roomID, opts)
		if err != nil {
			return "", err
		}
		if request.Requester != bAgent.OwnerID {
			return "", fmt.Errorf("the pending request is from %q, want %q", request.Requester, bAgent.OwnerID)
		}
		note := "pending " + request.ID
		if !borrowNoticeRE.MatchString(refusal) {
			note += " (B's agent did not mention the request)"
		}
		return note, nil
	}) {
		return
	}
	if !run.step("approve", func() (string, error) {
		return request.ID, c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/borrow/quota-requests/"+url.PathEscape(request.ID)+"/approve", map[string]any{}, nil)
	}) {
		return
	}
	since, err := smokeMaxSeq(ctx, c, roomID, opts)
	if err != nil {
		run.step("borrowed run", func() (string, error) { return "", err })
		return
	}
	_, bErr := bTurn(askA + "'Run ls -a in your workspace root only (not recursive) and reply with its exact output.' Then tell me its answer word for word.")
	var aReply string
	if !run.step("borrowed run", func() (string, error) {
		var err error
		aReply, err = smokeAgentReplyAfter(ctx, c, roomID, aAgent, since, opts)
		if err != nil {
			if bErr != nil {
				return "", fmt.Errorf("%w (B's turn: %v)", err, bErr)
			}
			return "", err
		}
		return truncateRunes(strings.Join(strings.Fields(aReply), " "), 80), nil
	}) {
		return
	}
	run.step("borrower isolation", func() (string, error) {
		if strings.Contains(aReply, "report.md") {
			return "", fmt.Errorf("the borrowed turn's workspace holds A's report.md: %q", truncateRunes(aReply, 160))
		}
		return "report.md absent", nil
	})
}

// smokeTeammateAgent resolves B's agent (--teammate-agent: provider or
// core/provider) and waits (AgentTimeout) until it is online: its bridge
// may have just started.
func smokeTeammateAgent(ctx context.Context, b *client, subject string, opts smokeOptions) (smokeRecipient, error) {
	deadline := time.Now().Add(opts.AgentTimeout)
	for {
		var mine struct {
			Agents []myAgent `json:"agents"`
		}
		err := b.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/my/agents", nil, &mine)
		if err == nil {
			rcpt, rerr := resolveSmokeAgent(opts.TeammateAgent, subject, mine.Agents)
			if rerr == nil {
				for _, a := range mine.Agents {
					if a.Provider == rcpt.Provider && (rcpt.Core == "" || a.Core == rcpt.Core) && a.Online {
						return rcpt, nil
					}
				}
			}
			err = rerr
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("the teammate's %s agent is not online", opts.TeammateAgent)
			}
			return smokeRecipient{}, err
		}
		select {
		case <-ctx.Done():
			return smokeRecipient{}, ctx.Err()
		case <-time.After(opts.pollEvery()):
		}
	}
}

// smokeNamesNote says how B's agent named A: by display name, or by id.
func smokeNamesNote(ctx context.Context, c *client, subject, reply string, opts smokeOptions) string {
	var page struct {
		Profiles []wire.UserProfile `json:"profiles"`
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/users/profiles?ids="+url.QueryEscape(subject), nil, &page); err != nil || len(page.Profiles) == 0 {
		return "A's profile unreadable"
	}
	name := strings.TrimSpace(page.Profiles[0].DisplayName)
	switch {
	case name != "" && name != subject && strings.Contains(reply, name):
		return fmt.Sprintf("A named %q", name)
	case strings.Contains(reply, subject):
		return "A named by id only"
	default:
		return "A not named"
	}
}

// smokeAccessRequest is one borrow access request as A lists it.
type smokeAccessRequest struct {
	ID        string `json:"requestId"`
	Requester string `json:"requester"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
}

// smokePendingAccessRequest is A's pending access request in the room
// (waiting briefly: the refusal raises it as B's turn ends).
func smokePendingAccessRequest(ctx context.Context, c *client, roomID string, opts smokeOptions) (smokeAccessRequest, error) {
	deadline := time.Now().Add(opts.StepTimeout)
	for {
		var out struct {
			Requests []smokeAccessRequest `json:"requests"`
		}
		err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/borrow/quota-requests?role=owner&status=pending&roomId="+url.QueryEscape(roomID), nil, &out)
		if err == nil {
			for _, r := range out.Requests {
				if r.Kind == "access" && r.Status == "pending" {
					return r, nil
				}
			}
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("A holds no pending access request in %s", roomID)
			}
			return smokeAccessRequest{}, err
		}
		select {
		case <-ctx.Done():
			return smokeAccessRequest{}, ctx.Err()
		case <-time.After(opts.pollEvery()):
		}
	}
}

// smokeMaxSeq is the room's newest sequence (A's view).
func smokeMaxSeq(ctx context.Context, c *client, roomID string, opts smokeOptions) (int64, error) {
	var page struct {
		Messages []smokeEvent `json:"messages"`
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/history?limit=1", nil, &page); err != nil {
		return 0, err
	}
	var max int64
	for _, m := range page.Messages {
		if m.Sequence > max {
			max = m.Sequence
		}
	}
	return max, nil
}

// smokeAgentReplyAfter is what rcpt's agent replied in the room after seq
// (a dispatched task's answer is posted there too).
func smokeAgentReplyAfter(ctx context.Context, c *client, roomID string, rcpt smokeRecipient, after int64, opts smokeOptions) (string, error) {
	var page struct {
		Messages []smokeEvent `json:"messages"`
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/history?limit=100", nil, &page); err != nil {
		return "", err
	}
	var texts []string
	for _, m := range page.Messages {
		if m.Sequence <= after {
			continue
		}
		got, fail := agentReplies(m, rcpt)
		if fail != "" {
			return "", fmt.Errorf("%s's job failed: %s", rcpt.OwnerID+"/"+rcpt.Provider, fail)
		}
		texts = append(texts, got...)
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("%s did not answer the borrowed task", rcpt.OwnerID+"/"+rcpt.Provider)
	}
	return strings.Join(texts, "\n"), nil
}

// teammateTokenFrom is the teammate token: the flag, else the environment.
func teammateTokenFrom(flagValue string, getenv func(string) string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(getenv(teammateTokenEnv))
}
