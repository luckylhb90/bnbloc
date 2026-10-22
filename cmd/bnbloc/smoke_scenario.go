package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The long-task scenario (`bnbloc smoke --agent X --scenario long-task`)
// checks what a real member's long task depends on, each property on its
// own line so a failure names what broke:
//
//	attachment      an uploaded brief.md reaches the agent (it answers the
//	                codeword inside)
//	workspace write the agent creates notes/plan.md in its workspace
//	between         (--between) a shell command run between the turns — a
//	                core publish, a sidecar restart — then the agent is
//	                online again
//	memory          the agent still knows the codeword without re-reading
//	                (its conversation survived)
//	workspace file  notes/plan.md still holds what it wrote (its files
//	                survived)
//	room tools      the agent still answers through its room tools (a core
//	                that restarts its sessions must not drop them); INFO
//	                when the agent had none before --between
//	session         every reply came from one agent session: "memory"
//	                alone passes for an agent that starts a new session
//	                each turn, since the room's history holds the codeword
//	                (docs/bugs/2026-10-21-sandboxed-sessions-reset-every-turn.md)
//	progress        INFO: agent_job_progress events seen during the turns
//
// A failed scenario keeps its room for debugging.

// scenarioLongTask names the long-task scenario.
const scenarioLongTask = "long-task"

// scenarioCodeword is "CW-<8 hex>" and its step marker "STEP1-<8 hex>".
func scenarioCodeword() (codeword, step string, err error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	h := hex.EncodeToString(buf)
	return "CW-" + h, "STEP1-" + h, nil
}

// runBetween runs --between through sh -c, its output to stderr.
func runBetween(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// smokeLongTask runs the scenario's checks on the open room. It reports
// every check it reached and fails the run when any failed.
func smokeLongTask(ctx context.Context, c *client, w *smokeWaiter, roomID, subject string, opts smokeOptions, run *smokeRun) {
	var rcpt smokeRecipient
	if !run.step("agent", func() (string, error) {
		var err error
		rcpt, err = smokeResolveAgent(ctx, c, subject, opts)
		return rcpt.OwnerID + "/" + rcpt.Provider, err
	}) {
		return
	}
	codeword, marker, err := scenarioCodeword()
	if err != nil {
		run.step("codeword", func() (string, error) { return "", err })
		return
	}
	turn := func(text string, attachmentIDs []string, want ...string) (string, error) {
		return smokeScenarioTurn(ctx, c, w, roomID, rcpt, text, attachmentIDs, opts, want...)
	}

	if !run.step("attachment", func() (string, error) {
		att, err := smokeUploadAttachment(ctx, c, roomID, "brief.md", "text/markdown",
			[]byte("# Brief\n\nThe codeword for this task is "+codeword+".\n"), opts.StepTimeout)
		if err != nil {
			return "", err
		}
		return turn("Read the attached brief.md and reply with only the codeword it contains.", []string{att.ID}, codeword)
	}) {
		return
	}
	if !run.step("workspace write", func() (string, error) {
		return turn("Create the file notes/plan.md in your workspace containing exactly "+marker+", then reply DONE.", nil, "DONE")
	}) {
		return
	}
	// Whether the agent has room tools now: checked again after --between.
	toolsBefore, _ := turn(roomToolsProbe, nil)
	hadTools := strings.Contains(toolsBefore, roomToolsOK)
	if opts.Between != "" {
		if !run.step("between", func() (string, error) {
			between := opts.RunBetween
			if between == nil {
				between = runBetween
			}
			if err := between(ctx, opts.Between); err != nil {
				return "", fmt.Errorf("%q: %w", opts.Between, err)
			}
			return opts.Between, smokeAgentOnline(ctx, c, rcpt, opts)
		}) {
			return
		}
	}
	// One turn answers both questions; each property is its own line.
	reply, replyErr := turn("Without re-reading any file, what codeword did I give you earlier? Then print the content of notes/plan.md. Reply as: <codeword> <file content>.", nil, codeword, marker)
	if replyErr != nil && reply != "" && strings.HasPrefix(replyErr.Error(), "the reply does not hold") {
		replyErr = nil // the agent answered: the checks below say what it lacked
	}
	run.step("memory", func() (string, error) {
		if replyErr != nil {
			return "", replyErr
		}
		if !strings.Contains(reply, codeword) {
			return "", fmt.Errorf("the reply does not hold the codeword %s (the conversation was lost?): %q", codeword, truncateRunes(reply, 120))
		}
		return codeword, nil
	})
	run.step("workspace file", func() (string, error) {
		if replyErr != nil {
			return "", replyErr
		}
		if !strings.Contains(reply, marker) {
			return "", fmt.Errorf("the reply does not hold notes/plan.md's %s (the workspace file was lost?): %q", marker, truncateRunes(reply, 120))
		}
		return marker, nil
	})
	if hadTools {
		run.step("room tools", func() (string, error) {
			reply, err := turn(roomToolsProbe, nil, roomToolsOK)
			if err != nil && reply != "" {
				return "", fmt.Errorf("the agent no longer reaches its room tools: %q", truncateRunes(reply, 120))
			}
			return roomToolsOK, err
		})
	} else {
		fmt.Fprintf(run.out, "INFO room tools: the agent had none before the change (not checked)\n")
	}
	if sessions := w.sessions[rcpt.OwnerID+"/"+rcpt.Provider]; len(sessions) == 0 {
		fmt.Fprintf(run.out, "INFO session: the replies named no agent session (not checked)\n")
	} else {
		run.step("session", func() (string, error) {
			agent := rcpt.OwnerID + "/" + rcpt.Provider
			if n := w.conversations(agent); n > 1 {
				return "", fmt.Errorf("the agent answered from %d sessions (%s): its conversation was not continued between turns", len(sessions), strings.Join(sessions, ", "))
			}
			if len(sessions) > 1 {
				// One conversation in several sessions: the core restarted and kept it,
				// and the bridge named each session's predecessor.
				return strings.Join(sessions, " -> ") + " (the core kept the conversation across the change)", nil
			}
			return sessions[0], nil
		})
	}
	fmt.Fprintf(run.out, "INFO progress events seen: %d\n", w.progress)
}

// roomToolsProbe asks the agent to use one of its room tools; roomToolsOK
// is the answer of an agent that could.
const (
	roomToolsOK    = "ROOMTOOLS-OK"
	roomToolsProbe = "Call your room_status tool. Reply " + roomToolsOK + " if it answered, or ROOMTOOLS-NONE if you have no such tool."
)

// smokeResolveAgent resolves --agent against the caller's agents.
func smokeResolveAgent(ctx context.Context, c *client, subject string, opts smokeOptions) (smokeRecipient, error) {
	var mine struct {
		Agents []myAgent `json:"agents"`
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/my/agents", nil, &mine); err != nil {
		return smokeRecipient{}, err
	}
	return resolveSmokeAgent(opts.Agent, subject, mine.Agents)
}

// smokeAgentOnline waits (up to AgentTimeout) until the caller's agent is
// online again after --between; another member's agent is not checked.
func smokeAgentOnline(ctx context.Context, c *client, rcpt smokeRecipient, opts smokeOptions) error {
	deadline := time.Now().Add(opts.AgentTimeout)
	for {
		var mine struct {
			Agents []myAgent `json:"agents"`
		}
		err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/my/agents", nil, &mine)
		if err == nil {
			own := false
			for _, a := range mine.Agents {
				if a.Provider == rcpt.Provider && (rcpt.Core == "" || a.Core == rcpt.Core) {
					own = true
					if a.Online {
						return nil
					}
				}
			}
			if !own {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the agent was not online again within %s", opts.AgentTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.pollEvery()):
		}
	}
}

// smokeUploadAttachment uploads one file the way the composer does.
func smokeUploadAttachment(ctx context.Context, c *client, roomID, name, contentType string, content []byte, timeout time.Duration) (wire.ChatAttachment, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return wire.ChatAttachment{}, err
	}
	if _, err := part.Write(content); err != nil {
		return wire.ChatAttachment{}, err
	}
	if err := mw.Close(); err != nil {
		return wire.ChatAttachment{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := c.doReader(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/attachments", &body, mw.FormDataContentType())
	if err != nil {
		return wire.ChatAttachment{}, err
	}
	var att wire.ChatAttachment
	if err := json.Unmarshal(raw, &att); err != nil || att.ID == "" {
		return wire.ChatAttachment{}, fmt.Errorf("upload answered no attachment id: %s", truncateRunes(string(raw), 120))
	}
	return att, nil
}

// smokeScenarioTurn sends text (with attachments) to the agent and collects
// its assistant replies: it ends once they hold every want, else once they
// settled (no new reply for opts.settle()) after the first one, else at
// AgentTimeout. A job-failure notice fails the turn.
func smokeScenarioTurn(ctx context.Context, c *client, w *smokeWaiter, roomID string, rcpt smokeRecipient, text string, attachmentIDs []string, opts smokeOptions, want ...string) (string, error) {
	msgID, err := smokeID()
	if err != nil {
		return "", err
	}
	body := map[string]any{"text": text, "atUnixMs": opts.Now().UnixMilli(), "clientMsgId": msgID, "recipient": rcpt}
	if len(attachmentIDs) > 0 {
		body["attachmentIds"] = attachmentIDs
	}
	if err := c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", body, nil); err != nil {
		return "", err
	}
	handle := rcpt.OwnerID + "/" + rcpt.Provider
	var got []string
	holds := func() bool {
		all := strings.Join(got, "\n")
		for _, s := range want {
			if !strings.Contains(all, s) {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(opts.AgentTimeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		wait := left
		if len(got) > 0 && wait > opts.settle() {
			wait = opts.settle()
		}
		_, err := w.wait(wait, "reply from "+handle, func(ev smokeEvent) (bool, error) {
			texts, fail := agentReplies(ev, rcpt)
			if fail != "" {
				return false, fmt.Errorf("agent job failed: %s", fail)
			}
			if len(texts) > 0 {
				w.noteSession(handle, agentSessionOf(ev), agentContinuesOf(ev))
			}
			got = append(got, texts...)
			return len(texts) > 0, nil
		})
		switch {
		case err == nil && len(want) > 0 && holds():
			return strings.Join(got, "\n"), nil
		case err == nil:
			continue
		case strings.HasPrefix(err.Error(), "no reply") && len(got) > 0:
			// Settled: nothing more came after the replies so far.
			reply := strings.Join(got, "\n")
			if !holds() {
				return reply, fmt.Errorf("the reply does not hold %s: %q", strings.Join(want, ", "), truncateRunes(reply, 120))
			}
			return reply, nil
		default:
			return strings.Join(got, "\n"), err
		}
	}
	reply := strings.Join(got, "\n")
	if reply == "" {
		return "", fmt.Errorf("no reply from %s within %s", handle, opts.AgentTimeout)
	}
	if !holds() {
		return reply, fmt.Errorf("the reply does not hold %s: %q", strings.Join(want, ", "), truncateRunes(reply, 120))
	}
	return reply, nil
}

// agentSessionOf is the agent session an agent_messages event came from.
func agentSessionOf(ev smokeEvent) string {
	var batch struct {
		AgentSessionID string `json:"agentSessionId"`
	}
	_ = json.Unmarshal(ev.Payload, &batch)
	return batch.AgentSessionID
}

// agentContinuesOf is the session an agent_messages event's session says it took
// over ("" for none): the bridge adds it to the replies of a continued session.
func agentContinuesOf(ev smokeEvent) string {
	var batch struct {
		Messages []struct {
			Payload struct {
				Continues string `json:"continues"`
			} `json:"payload"`
		} `json:"messages"`
	}
	if json.Unmarshal(ev.Payload, &batch) != nil {
		return ""
	}
	for _, m := range batch.Messages {
		if m.Payload.Continues != "" {
			return m.Payload.Continues
		}
	}
	return ""
}

// agentReplies reads an agent_messages event: the assistant texts of rcpt's
// agent, and a job failure's reason and text ("" for none).
func agentReplies(ev smokeEvent, rcpt smokeRecipient) (texts []string, fail string) {
	if ev.Kind != wire.KindAgentMessages {
		return nil, ""
	}
	var batch struct {
		Messages []struct {
			Payload struct {
				Role        string `json:"role"`
				Text        string `json:"text"`
				OwnerUserID string `json:"ownerUserId"`
				Provider    string `json:"provider"`
				FailReason  string `json:"failReason"`
				Notice      string `json:"notice"`
			} `json:"payload"`
		} `json:"messages"`
	}
	if json.Unmarshal(ev.Payload, &batch) != nil {
		return nil, ""
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
			return texts, fmt.Sprintf("(%s) %s", p.FailReason, p.Text)
		}
		if p.Notice != "" {
			continue // a notice (a context reset, an undelivered file) is no reply
		}
		texts = append(texts, p.Text)
	}
	return texts, ""
}
