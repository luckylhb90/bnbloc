package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The steer scenario (`bnbloc smoke --agent X --scenario steer`) checks that a message sent
// while the agent works joins that work (docs/prd/2026-10-25-steer-a-running-turn.md), each
// property on its own line so a failure names what broke:
//
//	started    the agent starts a task that takes a while (it runs a command that sleeps)
//	joined     a second message sent while it runs settles as steered into the first
//	           job (the job's steeredInto), not queued behind it
//	one reply  the first job's reply answers both messages: it holds the first task's word
//	           and the second message's word
//	after      a message sent once the turn ended is an ordinary job, not steered
//
// An agent whose core takes no steering is reported (INFO), not failed: its second message
// queues, which is the contract for such a core. A failed scenario keeps its room.

// scenarioSteer names the steer scenario.
const scenarioSteer = "steer"

// steerJob is the part of an agent job the scenario reads.
type steerJob struct {
	JobID       string `json:"jobId"`
	MessageSeq  int64  `json:"messageSeq"`
	Status      string `json:"status"`
	SteeredInto string `json:"steeredInto"`
	// Error and Reason say why a job that ended did not do its work.
	Error  string `json:"error"`
	Reason string `json:"failureReason"`
}

// failedHow is how a job that ended without replying says so ("" while it may still run).
func (j steerJob) failedHow() string {
	switch j.Status {
	case "failed", "cancelled", "timed_out":
		return strings.TrimSpace(j.Status + " " + j.Reason + ": " + j.Error)
	}
	return ""
}

// smokeSteer runs the scenario's checks on the open room.
func smokeSteer(ctx context.Context, c *client, w *smokeWaiter, roomID, subject string, opts smokeOptions, run *smokeRun) {
	var rcpt smokeRecipient
	if !run.step("agent", func() (string, error) {
		var err error
		rcpt, err = smokeResolveAgent(ctx, c, subject, opts)
		return rcpt.OwnerID + "/" + rcpt.Provider, err
	}) {
		return
	}
	first, second, err := steerWords()
	if err != nil {
		run.step("words", func() (string, error) { return "", err })
		return
	}
	var firstSeq, secondSeq int64
	send := func(text string) (int64, error) {
		msgID, err := smokeID()
		if err != nil {
			return 0, err
		}
		body := map[string]any{"text": text, "atUnixMs": opts.Now().UnixMilli(), "clientMsgId": msgID, "recipient": rcpt}
		var out struct {
			Sequence int64 `json:"sequence"`
		}
		err = c.call(ctx, opts.StepTimeout, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", body, &out)
		return out.Sequence, err
	}
	jobs := func() ([]steerJob, error) {
		var out struct {
			Jobs []steerJob `json:"jobs"`
		}
		err := c.call(ctx, opts.StepTimeout, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/agent-jobs", nil, &out)
		return out.Jobs, err
	}
	jobOf := func(seq int64) (steerJob, bool) {
		all, err := jobs()
		if err != nil {
			return steerJob{}, false
		}
		for _, j := range all {
			if j.MessageSeq == seq {
				return j, true
			}
		}
		return steerJob{}, false
	}

	if !run.step("started", func() (string, error) {
		var err error
		firstSeq, err = send("Run the shell command `for i in 1 2 3 4 5 6 7 8; do sleep 3; echo step $i; done` and wait for it to finish, then reply with the word " + first + " and nothing else.")
		if err != nil {
			return "", err
		}
		// Wait until the agent is working on it (its job reports running), then a moment
		// more so the turn is well under way when the second message comes.
		deadline := time.Now().Add(opts.AgentTimeout)
		for time.Now().Before(deadline) {
			if j, ok := jobOf(firstSeq); ok && (j.Status == "running" || j.Status == "reflowing") {
				time.Sleep(6 * time.Second)
				return fmt.Sprintf("job %s %s", j.JobID, j.Status), nil
			} else if ok && j.failedHow() != "" {
				return "", fmt.Errorf("the task's job ended before it ran: %s", j.failedHow())
			}
			time.Sleep(opts.pollEvery())
		}
		return "", fmt.Errorf("the agent did not start the task within %s", opts.AgentTimeout)
	}) {
		return
	}

	steered, queued := false, false
	if !run.step("joined", func() (string, error) {
		var err error
		secondSeq, err = send("Also end your reply with the word " + second + ".")
		if err != nil {
			return "", err
		}
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if j, ok := jobOf(secondSeq); ok {
				switch {
				case j.SteeredInto != "":
					first, _ := jobOf(firstSeq)
					if j.SteeredInto != first.JobID {
						return "", fmt.Errorf("the second message joined job %s, not the running one (%s)", j.SteeredInto, first.JobID)
					}
					steered = true
					return "steered into " + j.SteeredInto, nil
				case j.Status == "replied" || j.failedHow() != "":
					return "", fmt.Errorf("the second message's job ended %s without joining the running turn", cmp.Or(j.failedHow(), j.Status))
				}
			}
			time.Sleep(opts.pollEvery())
		}
		// Not steered within the wait: it is queued behind the turn. That is the contract
		// of a core that takes no steering, so it is reported, not failed.
		fmt.Fprintf(run.out, "INFO joined: the second message was queued behind the running turn (this agent's core takes no steering)\n")
		queued = true
		return "", smokeWarning{err: fmt.Errorf("not steered")}
	}) && !queued {
		return
	}

	if queued {
		// Both turns still complete, one after the other: the first task's word (its command ran,
		// wherever the agent runs commands), then the queued message's.
		run.step("queued replies", func() (string, error) {
			reply, err := collectReply(ctx, w, rcpt, opts, first, second)
			if err != nil {
				return "", err
			}
			return reply, nil
		})
		return
	}

	if steered {
		run.step("one reply", func() (string, error) {
			// The one reply answers both messages: the first task's word, then the second's.
			reply, err := collectReply(ctx, w, rcpt, opts, first, second)
			if err != nil {
				return "", err
			}
			if !strings.Contains(reply, first) || !strings.Contains(reply, second) {
				return "", fmt.Errorf("the reply does not answer both messages (wants %s and %s): %q", first, second, truncateRunes(reply, 160))
			}
			return reply, nil
		})
		run.step("after", func() (string, error) {
			// Once the turn ended, a new message is an ordinary job.
			time.Sleep(3 * time.Second)
			seq, err := send("Reply with the word AFTER-" + first + ".")
			if err != nil {
				return "", err
			}
			deadline := time.Now().Add(opts.AgentTimeout)
			for time.Now().Before(deadline) {
				if j, ok := jobOf(seq); ok {
					if j.SteeredInto != "" {
						return "", fmt.Errorf("a message sent after the turn ended was steered into %s", j.SteeredInto)
					}
					if j.Status == "replied" {
						return "an ordinary job: " + j.JobID, nil
					}
					if j.Status == "failed" || j.Status == "cancelled" || j.Status == "timed_out" {
						return "", fmt.Errorf("the job ended %s", j.Status)
					}
				}
				time.Sleep(opts.pollEvery())
			}
			return "", fmt.Errorf("no reply within %s", opts.AgentTimeout)
		})
	}
}

// collectReply gathers the agent's replies until they hold every want, or settle.
func collectReply(ctx context.Context, w *smokeWaiter, rcpt smokeRecipient, opts smokeOptions, want ...string) (string, error) {
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
	for time.Now().Before(deadline) && ctx.Err() == nil {
		wait := time.Until(deadline)
		if len(got) > 0 && wait > opts.settle() {
			wait = opts.settle()
		}
		_, err := w.wait(wait, "reply from "+handle, func(ev smokeEvent) (bool, error) {
			texts, fail := agentReplies(ev, rcpt)
			if fail != "" {
				return false, fmt.Errorf("agent job failed: %s", fail)
			}
			got = append(got, texts...)
			return len(texts) > 0, nil
		})
		switch {
		case err == nil && holds():
			return strings.Join(got, "\n"), nil
		case err == nil:
			continue
		case strings.HasPrefix(err.Error(), "no reply") && len(got) > 0:
			return strings.Join(got, "\n"), nil
		default:
			return strings.Join(got, "\n"), err
		}
	}
	return strings.Join(got, "\n"), fmt.Errorf("no reply from %s within %s", handle, opts.AgentTimeout)
}

// steerWords are the two words the scenario looks for: "S1-<8 hex>", "S2-<8 hex>".
func steerWords() (first, second string, err error) {
	codeword, _, err := scenarioCodeword()
	if err != nil {
		return "", "", err
	}
	h := strings.TrimPrefix(codeword, "CW-")
	return "S1-" + h, "S2-" + h, nil
}
