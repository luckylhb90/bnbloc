package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// `bnbloc smoke` is the post-deploy check of the room message path against a
// running gateway, exercised the way the client does it: the room SSE stream
// is opened FIRST and kept open while the chat is sent, so the echo (and the
// optional agent reply) must arrive on the same long-lived connection — a
// one-shot request would hide the stream bugs AGENTS.md lists.
//
//	auth        resolve the session's subject; GET /v1/users/{subject}/rooms
//	versions    GET /v1/platform/versions: every instance on one commit (WARN, not
//	            FAIL, when the caller is not a platform super-admin)
//	create room POST /v1/rooms (private, "smoke-<utc>-<rand>")
//	open stream GET /v1/rooms/{id}/events?allChannels=1&presence=0
//	chat echo   POST /v1/rooms/{id}/chat {clientMsgId}; the chat_message echo carries it
//	history     GET /v1/rooms/{id}/history contains that clientMsgId
//	agent reply (--agent) chat addressed to the agent; an assistant agent_messages arrives
//	            (--scenario long-task / two-members replaces it with the
//	            scenario's checks, smoke_scenario.go / smoke_two_members.go)
//	delete room DELETE /v1/rooms/{id} (always, unless --keep or a failed scenario)

// smokeOptions is one `bnbloc smoke` run.
type smokeOptions struct {
	// Agent addresses the optional agent-reply step ("" skips it).
	Agent string
	// AgentTimeout bounds the wait for the agent reply.
	AgentTimeout time.Duration
	// StepTimeout bounds every other wait (stream open, echo).
	StepTimeout time.Duration
	// Keep skips the room deletion.
	Keep bool
	// Scenario runs a scenario instead of the single agent reply
	// ("long-task"; needs Agent).
	Scenario string
	// Between is a shell command the long-task scenario runs between its
	// turns (an upgrade); it must return once the change is in effect.
	Between string
	// TeammateToken / TeammateAgent: the two-members scenario's second
	// member (an API token's subject) and their agent.
	TeammateToken string
	TeammateAgent string
	// RunBetween runs Between (nil: sh -c; tests replace it).
	RunBetween func(ctx context.Context, command string) error
	// PollEvery spaces the online checks after Between (0: 3s).
	PollEvery time.Duration
	// Settle is how long a scenario turn waits for more replies after the
	// last one before judging them (0: 30s).
	Settle time.Duration
	Now    func() time.Time
}

func (o smokeOptions) settle() time.Duration {
	if o.Settle > 0 {
		return o.Settle
	}
	return 30 * time.Second
}

func (o smokeOptions) pollEvery() time.Duration {
	if o.PollEvery > 0 {
		return o.PollEvery
	}
	return 3 * time.Second
}

func runSmoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	agent := fs.String("agent", "", "also wait for a reply from this agent: provider | core/provider | target id of your own agent, or owner/provider | owner/core/provider")
	timeout := fs.Duration("timeout", 120*time.Second, "how long to wait for the agent reply (each turn of a scenario; default 4m for one)")
	scenario := fs.String("scenario", "", `run a scenario instead of the single agent reply: "long-task" or "two-members" (both need --agent)`)
	teammateToken := fs.String("teammate-token", "", "two-members: the teammate's API token (prefer "+teammateTokenEnv+": argv is visible to other processes)")
	teammateAgent := fs.String("teammate-agent", "", "two-members: the teammate's agent (provider or core/provider)")
	between := fs.String("between", "", "long-task: a shell command run between its turns (e.g. a core publish); it must return once the change is in effect")
	keep := fs.Bool("keep", false, "keep the smoke room (skip the cleanup)")
	alertOnFailure := fs.Bool("alert-on-failure", false, "when the run fails, alert platform admins through the installed notifiers (POST /v1/platform/alerts; needs a super-admin session)")
	alertTo := fs.String("alert-to", "", "comma-separated alert recipients (default: every platform super-admin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("smoke: unexpected argument %q", fs.Arg(0))
	}
	// Ctrl-C aborts the current wait; the deferred cleanup still deletes the room.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := gw.client()
	fmt.Printf("smoke against %s\n", c.cfg.Gateway)
	opts := smokeOptions{Agent: strings.TrimSpace(*agent), AgentTimeout: *timeout, StepTimeout: apiTimeout, Keep: *keep, Now: time.Now,
		Scenario: strings.TrimSpace(*scenario), Between: strings.TrimSpace(*between),
		TeammateToken: teammateTokenFrom(*teammateToken, os.Getenv), TeammateAgent: strings.TrimSpace(*teammateAgent)}
	switch {
	case opts.Scenario != "" && opts.Scenario != scenarioLongTask && opts.Scenario != scenarioTwoMembers:
		return fmt.Errorf("smoke: unknown --scenario %q (want %s or %s)", opts.Scenario, scenarioLongTask, scenarioTwoMembers)
	case opts.Scenario != "" && opts.Agent == "":
		return fmt.Errorf("smoke: --scenario %s needs --agent", opts.Scenario)
	case opts.Between != "" && opts.Scenario != scenarioLongTask:
		return errors.New("smoke: --between needs --scenario long-task")
	case opts.Scenario == scenarioTwoMembers && (opts.TeammateToken == "" || opts.TeammateAgent == ""):
		return fmt.Errorf("smoke: --scenario %s needs --teammate-token (or %s) and --teammate-agent", scenarioTwoMembers, teammateTokenEnv)
	}
	switch {
	case opts.Scenario == scenarioTwoMembers && !flagSet(fs, "timeout"):
		opts.AgentTimeout = 6 * time.Minute
	case opts.Scenario != "" && !flagSet(fs, "timeout"):
		opts.AgentTimeout = 4 * time.Minute
	}
	run := &smokeRun{out: os.Stdout}
	err := smokeChecks(ctx, c, opts, run)
	if err != nil && *alertOnFailure {
		alertSmokeFailure(c, run, err, opts.Agent, splitList(*alertTo), os.Stdout, os.Stderr)
	}
	return err
}

// alertSmokeFailure alerts admins about a failed run. It runs after the
// room cleanup, on its own context (the run's may be cancelled). An alert
// that cannot be sent is reported on errOut; the run still fails with its
// own error.
func alertSmokeFailure(c *client, run *smokeRun, runErr error, agent string, to []string, out, errOut io.Writer) {
	a := smokeAlert(c.cfg.Gateway, run.failures, runErr, agent)
	a.Recipients = to
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	res, err := sendAlert(ctx, c, a)
	if err != nil {
		fmt.Fprintf(errOut, "bnbloc: smoke alert NOT sent: %v\n", err)
		return
	}
	fmt.Fprintf(out, "ALERT %s queued for %s via %s\n", res.ID, strings.Join(res.Recipients, ", "), strings.Join(res.Notifiers, ", "))
}

// errSmokeFailed is returned when any step failed (the steps already printed why).
var errSmokeFailed = errors.New("smoke failed")

// smokeRun prints one PASS/FAIL/SKIP line per step and remembers failures.
type smokeRun struct {
	out    io.Writer
	failed bool
	// failures are the "step: reason" of every failed step (the alert body).
	failures []string
}

// step runs fn and prints "PASS name (Nms)[: detail]" or "FAIL name: reason".
func (r *smokeRun) step(name string, fn func() (string, error)) bool {
	start := time.Now()
	detail, err := fn()
	var warn smokeWarning
	if errors.As(err, &warn) {
		fmt.Fprintf(r.out, "WARN %s: %v\n", name, warn.err)
		return true
	}
	if err != nil {
		r.failed = true
		r.failures = append(r.failures, fmt.Sprintf("FAIL %s: %v", name, err))
		fmt.Fprintf(r.out, "FAIL %s: %v\n", name, err)
		return false
	}
	line := fmt.Sprintf("PASS %s (%dms)", name, time.Since(start).Milliseconds())
	if detail != "" {
		line += ": " + detail
	}
	fmt.Fprintln(r.out, line)
	return true
}

func (r *smokeRun) skip(what string) { fmt.Fprintf(r.out, "SKIP %s\n", what) }

// smokeWarning makes a step print WARN instead of FAIL: the check could not
// run for a reason that is not a deployment fault (e.g. the caller's role).
type smokeWarning struct{ err error }

func (w smokeWarning) Error() string { return w.err.Error() }

// runSmokeChecks runs every step against c. A failed step ends the chain (the
// later steps depend on it); the room is deleted on every path unless Keep.
func runSmokeChecks(ctx context.Context, c *client, opts smokeOptions, out io.Writer) error {
	return smokeChecks(ctx, c, opts, &smokeRun{out: out})
}

// smokeChecks is runSmokeChecks on a caller-held run (which keeps the
// failures for an alert).
func smokeChecks(ctx context.Context, c *client, opts smokeOptions, run *smokeRun) (result error) {
	var subject string
	if !run.step("auth", func() (string, error) {
		s, err := smokeAuth(ctx, c, opts.StepTimeout)
		subject = s
		return "subject " + s, err
	}) {
		return errSmokeFailed
	}

	// Version skew does not stop the message-path checks: they still run, and
	// the run fails at the end.
	run.step("versions", func() (string, error) { return smokeVersions(ctx, c, opts.StepTimeout) })

	roomID, err := smokeRoomID(opts.Now())
	if err != nil {
		return err
	}
	if !run.step("create room", func() (string, error) {
		return roomID, c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms",
			map[string]string{"roomId": roomID, "name": roomID, "description": "bnbloc smoke (safe to delete)", "visibility": "private"}, nil)
	}) {
		return errSmokeFailed
	}
	defer func() {
		if opts.Keep {
			run.skip("delete room (--keep): " + roomID)
			return
		}
		if opts.Scenario != "" && run.failed {
			run.skip("delete room (kept: the scenario failed): " + roomID)
			return
		}
		// Cleanup runs even after an interrupt, so it gets its own context.
		if !run.step("delete room", func() (string, error) {
			return "", c.call(context.Background(), opts.StepTimeout, http.MethodDelete, "/v1/rooms/"+url.PathEscape(roomID), nil, nil)
		}) && result == nil {
			result = errSmokeFailed
		}
	}()

	streamCtx, closeStream := context.WithCancel(ctx)
	defer closeStream()
	var events <-chan smokeEvent
	var streamErr <-chan error
	if !run.step("open stream", func() (string, error) {
		var err error
		events, streamErr, err = openSmokeStream(streamCtx, c, roomID, opts.StepTimeout)
		return "", err
	}) {
		return errSmokeFailed
	}
	w := &smokeWaiter{ctx: ctx, events: events, streamErr: streamErr}

	clientMsgID, err := smokeID()
	if err != nil {
		return err
	}
	if !run.step("chat echo", func() (string, error) {
		var sent struct {
			Sequence int64 `json:"sequence"`
		}
		if err := c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", map[string]any{
			"text": "bnbloc smoke " + opts.Now().UTC().Format(time.RFC3339), "atUnixMs": opts.Now().UnixMilli(), "clientMsgId": clientMsgID,
		}, &sent); err != nil {
			return "", err
		}
		ev, err := w.wait(opts.StepTimeout, "chat_message echo", func(ev smokeEvent) (bool, error) {
			return ev.Kind == wire.KindChatMessage && payloadString(ev.Payload, "clientMsgId") == clientMsgID, nil
		})
		if err != nil {
			return "", err
		}
		if sent.Sequence != 0 && ev.Sequence != sent.Sequence {
			return "", fmt.Errorf("echo sequence %d != sent sequence %d", ev.Sequence, sent.Sequence)
		}
		return fmt.Sprintf("seq %d", ev.Sequence), nil
	}) {
		return errSmokeFailed
	}

	if !run.step("history", func() (string, error) {
		var page struct {
			Messages []smokeEvent `json:"messages"`
		}
		if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/history?limit=50", nil, &page); err != nil {
			return "", err
		}
		for _, m := range page.Messages {
			if m.Kind == wire.KindChatMessage && payloadString(m.Payload, "clientMsgId") == clientMsgID {
				return fmt.Sprintf("seq %d", m.Sequence), nil
			}
		}
		return "", fmt.Errorf("clientMsgId %s not in history (%d messages)", clientMsgID, len(page.Messages))
	}) {
		return errSmokeFailed
	}

	if opts.Scenario == scenarioLongTask {
		smokeLongTask(ctx, c, w, roomID, subject, opts, run)
	} else if opts.Scenario == scenarioTwoMembers {
		smokeTwoMembers(ctx, c, w, roomID, subject, opts, run)
	} else if opts.Agent == "" {
		run.skip("agent reply (pass --agent)")
	} else if !run.step("agent reply", func() (string, error) {
		return smokeAgentReply(ctx, c, w, roomID, subject, opts)
	}) {
		return errSmokeFailed
	}
	if run.failed { // a non-blocking step (versions) failed
		return errSmokeFailed
	}
	return nil
}

// smokeAuth resolves the session subject and proves the gateway accepts the
// session for it with an authenticated, subject-scoped read.
func smokeAuth(ctx context.Context, c *client, timeout time.Duration) (string, error) {
	id, err := c.identity()
	if err != nil {
		return "", err
	}
	subject := id.Subject
	if err := c.call(ctx, timeout, http.MethodGet, "/v1/users/"+url.PathEscape(subject)+"/rooms", nil, nil); err != nil {
		return "", err
	}
	return subject, nil
}

// smokeRoomID is "smoke-<utc yyyymmdd-hhmmss>-<4 hex>": recognizable, sortable,
// and unique across concurrent runs.
func smokeRoomID(now time.Time) (string, error) {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return "smoke-" + now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(suffix), nil
}

// smokeID is a random client message id (128 bits, hex).
func smokeID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "smoke-" + hex.EncodeToString(buf), nil
}

// smokeEvent is the realtime event shape shared by the SSE stream and the
// history endpoint ({sequence, kind, payload}).
type smokeEvent struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

func payloadString(raw json.RawMessage, key string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// openSmokeStream opens the room SSE stream as a non-presence subscriber
// (like the bridge, so the smoke does not churn the roster) and returns once
// the gateway answered 200. Events arrive on the first channel; the second
// carries the stream's end (nil error when ctx was cancelled).
func openSmokeStream(ctx context.Context, c *client, roomID string, timeout time.Duration) (<-chan smokeEvent, <-chan error, error) {
	events := make(chan smokeEvent, 64)
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		err := c.stream(ctx, "/v1/rooms/"+url.PathEscape(roomID)+"/events?allChannels=1&presence=0", func(r io.Reader) error {
			close(ready)
			return readSSE(ctx, r, events)
		})
		if ctx.Err() != nil {
			err = nil
		} else if err == nil {
			err = errors.New("the event stream closed")
		}
		done <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return events, done, nil
	case err := <-done:
		if err == nil {
			err = ctx.Err()
		}
		return nil, nil, err
	case <-timer.C:
		return nil, nil, fmt.Errorf("the event stream did not open within %s", timeout)
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// readSSE decodes the room event stream (internal/sse) into events until
// the stream ends (nil) or fails.
func readSSE(ctx context.Context, r io.Reader, out chan<- smokeEvent) error {
	rd := sse.NewReader(r)
	for {
		raw, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var ev smokeEvent
		if json.Unmarshal(raw.Data, &ev) != nil {
			continue // not a room event (never sent by realtime); ignore
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// smokeWaiter waits on the open stream for a matching event. It counts the
// agent_job_progress events that pass by (a live turn's activity).
type smokeWaiter struct {
	ctx       context.Context
	events    <-chan smokeEvent
	streamErr <-chan error
	progress  int
}

// wait returns the first event match accepts; match may also fail the wait.
func (w *smokeWaiter) wait(timeout time.Duration, what string, match func(smokeEvent) (bool, error)) (smokeEvent, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-w.events:
			if ev.Kind == wire.EventRoomDeleted {
				return ev, errors.New("the room was deleted while waiting")
			}
			if ev.Kind == wire.EventAgentJobProgress {
				w.progress++
			}
			ok, err := match(ev)
			if err != nil {
				return ev, err
			}
			if ok {
				return ev, nil
			}
		case err := <-w.streamErr:
			if err == nil {
				err = errors.New("the event stream closed")
			}
			return smokeEvent{}, fmt.Errorf("waiting for %s: %w", what, err)
		case <-timer.C:
			return smokeEvent{}, fmt.Errorf("no %s within %s", what, timeout)
		case <-w.ctx.Done():
			return smokeEvent{}, w.ctx.Err()
		}
	}
}

// smokeRecipient is the chat recipient shape the web composer sends.
type smokeRecipient struct {
	OwnerID  string `json:"ownerId"`
	Provider string `json:"provider"`
	Core     string `json:"core,omitempty"`
}

// myAgent is one row of GET /v1/my/agents.
type myAgent struct {
	Core     string `json:"core"`
	Provider string `json:"provider"`
	TargetID string `json:"targetId"`
	Online   bool   `json:"online"`
}

// resolveSmokeAgent maps --agent to a recipient. The caller's own agents (GET
// /v1/my/agents) are matched first by target id, core/provider, or provider;
// anything else is read as owner/provider or owner/core/provider.
func resolveSmokeAgent(spec, subject string, mine []myAgent) (smokeRecipient, error) {
	var hits []myAgent
	for _, a := range mine {
		if spec == a.TargetID || spec == a.Provider || (a.Core != "" && spec == a.Core+"/"+a.Provider) {
			hits = append(hits, a)
		}
	}
	switch {
	case len(hits) == 1:
		if !hits[0].Online {
			return smokeRecipient{}, fmt.Errorf("your agent %s is offline (no bridge serves it); start the desktop app or bridge", spec)
		}
		return smokeRecipient{OwnerID: subject, Provider: hits[0].Provider, Core: hits[0].Core}, nil
	case len(hits) > 1:
		names := make([]string, 0, len(hits))
		for _, a := range hits {
			names = append(names, a.TargetID)
		}
		return smokeRecipient{}, fmt.Errorf("--agent %s matches several of your agents (%s); pass the target id", spec, strings.Join(names, ", "))
	}
	parts := strings.Split(spec, "/")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return smokeRecipient{}, fmt.Errorf("--agent %q: empty segment", spec)
		}
	}
	switch len(parts) {
	case 2:
		return smokeRecipient{OwnerID: parts[0], Provider: parts[1]}, nil
	case 3:
		return smokeRecipient{OwnerID: parts[0], Core: parts[1], Provider: parts[2]}, nil
	}
	return smokeRecipient{}, fmt.Errorf("--agent %q is not one of your agents (GET /v1/my/agents) nor owner/provider or owner/core/provider", spec)
}

// smokeAgentReply sends a chat addressed to the agent and waits for its
// assistant reply on the open stream. A job-failure notice fails the step.
func smokeAgentReply(ctx context.Context, c *client, w *smokeWaiter, roomID, subject string, opts smokeOptions) (string, error) {
	var mine struct {
		Agents []myAgent `json:"agents"`
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/my/agents", nil, &mine); err != nil {
		return "", err
	}
	rcpt, err := resolveSmokeAgent(opts.Agent, subject, mine.Agents)
	if err != nil {
		return "", err
	}
	msgID, err := smokeID()
	if err != nil {
		return "", err
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", map[string]any{
		"text":        "bnbloc smoke: reply with the single word OK.",
		"atUnixMs":    opts.Now().UnixMilli(),
		"clientMsgId": msgID,
		"recipient":   rcpt,
	}, nil); err != nil {
		return "", err
	}
	handle := rcpt.OwnerID + "/" + rcpt.Provider
	var reply string
	_, err = w.wait(opts.AgentTimeout, "reply from "+handle, func(ev smokeEvent) (bool, error) {
		if ev.Kind != wire.KindAgentMessages {
			return false, nil
		}
		var batch struct {
			Messages []struct {
				Payload struct {
					Role        string `json:"role"`
					Text        string `json:"text"`
					OwnerUserID string `json:"ownerUserId"`
					Provider    string `json:"provider"`
					FailReason  string `json:"failReason"`
				} `json:"payload"`
			} `json:"messages"`
		}
		if json.Unmarshal(ev.Payload, &batch) != nil {
			return false, nil
		}
		for _, m := range batch.Messages {
			p := m.Payload
			if (p.Role != "" && p.Role != "assistant") || p.Text == "" {
				continue
			}
			if (p.OwnerUserID != "" && p.OwnerUserID != rcpt.OwnerID) || (p.Provider != "" && p.Provider != rcpt.Provider) {
				continue
			}
			if p.FailReason != "" {
				return false, fmt.Errorf("agent job failed (%s): %s", p.FailReason, p.Text)
			}
			reply = p.Text
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %q", handle, truncateRunes(reply, 60)), nil
}

func truncateRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// flagSet reports whether the command line set flag name.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
