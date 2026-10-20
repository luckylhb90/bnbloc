package roomtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// components try (docs/prd/2026-10-12-experts-and-teams.md, "Runtime gap
// closure" 4): the Expert builder tries an unsaved draft for real. The
// draft in the workspace is packed and validated here (the agent fixes what
// the validator says), staged on the gateway as an ephemeral draft of the
// bound job (POST /v1/content-drafts with the job grant: never listed,
// expiring), and a sample task is dispatched to another agent of the room
// wearing it, through the same dispatch door and guards as ask_agent.

func tryDraft(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		Dir         string `json:"dir"`
		Task        string `json:"task"`
		Handle      string `json:"handle"`
		WaitSeconds *int   `json:"wait_seconds"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	in.Task = strings.TrimSpace(in.Task)
	switch {
	case in.Task == "":
		return toolError("task is required: a realistic sample task for the draft")
	case len(in.Task) > maxTaskBytes:
		return toolError("task is too long (at most %d bytes)", maxTaskBytes)
	}
	job, grant, refused := jobGrant(env, "components")
	if refused != nil {
		return *refused
	}
	if stop := teamStopped(env, job); stop != nil {
		return *stop
	}
	dir, err := cleanDir(in.Dir)
	if err != nil {
		return toolError("%v", err)
	}
	ws := env.Session.workspace()
	if ws == nil {
		return toolError("components try cannot read your workspace: this agent's core does not expose its files. Answer the sample task in the draft's role yourself and say it was not a real try")
	}
	bundle, _, err := packDir(ctx, ws, dir)
	if err != nil {
		return toolError("%v", err)
	}
	pkg, err := contentpkg.Validate(bundle, "", contentpkg.Options{})
	if err != nil {
		return toolError("the draft in %s is not valid yet: %v", dir, err)
	}
	if k := pkg.Manifest.Kind; k != contentpkg.KindExpert && k != contentpkg.KindSkill {
		return toolError("only an expert or a skill pack can be tried (an agent wears it); %s is a %s", dir, k)
	}
	target, refusedTarget := draftTarget(ctx, env, job, in.Handle)
	if refusedTarget != nil {
		return *refusedTarget
	}
	raw, err := env.Gateway.Do(ctx, "POST", "/v1/content-drafts?"+grant.Encode(), RawBody{ContentType: "application/x-tar", Data: bundle})
	if err != nil {
		return refusal("staging "+dir, err)
	}
	var draft wire.ContentDraft
	if err := json.Unmarshal(raw, &draft); err != nil || draft.ID == "" {
		return failure("staging %s answered an unreadable result", dir)
	}
	label := target.Handle + " (trying " + firstNonEmptyString(draft.DisplayName, draft.Name) + ")"
	out, res := dispatchTask(ctx, env, job, target, label, wire.DispatchRequest{Text: in.Task, Draft: draft.ID}, in.WaitSeconds)
	if out != nil {
		out["draft"] = map[string]any{"name": draft.Name, "kind": draft.Kind, "version": draft.Version, "expiresAtMs": draft.ExpiresAtMs}
		out["note"] = fmt.Sprintf("This was a real try of the unsaved draft %s, worn by %s; nothing was saved. Show the person the result, revise the draft if needed, and save it only when they confirm.", draft.Name, target.Handle)
	}
	return res
}

// draftTarget picks the agent that wears the draft: the named one, else
// the requester's own other agent, else another online agent of the room
// (never the one this session drives: a self-dispatch is refused, and the
// agent is busy answering this very request).
func draftTarget(ctx context.Context, env *Env, job Job, handle string) (roomAgent, *Result) {
	agents, err := roomAgents(ctx, env)
	if err != nil {
		r := refusal("listing the room's agents", err)
		return roomAgent{}, &r
	}
	if h := strings.TrimSpace(handle); h != "" {
		target, ok := resolveHandle(agents, h)
		if !ok {
			r := toolError("no agent %q in this room right now; available: %s", h, handles(agents))
			return roomAgent{}, &r
		}
		if target.You {
			r := toolError("%s is you: a draft is tried by another agent of the room", target.Handle)
			return roomAgent{}, &r
		}
		return target, nil
	}
	var other *roomAgent
	for i := range agents {
		a := agents[i]
		if a.You || !a.Online {
			continue
		}
		if a.Owner == job.RequesterUserID {
			return a, nil
		}
		if other == nil {
			other = &agents[i]
		}
	}
	if other != nil {
		return *other, nil
	}
	r := toolError("no other agent in this room can try the draft right now. Tell the person: adding a platform agent or another of their agents to the room makes a real try possible. Meanwhile answer the sample task in the draft's role yourself and say plainly it was role-play, not a real try")
	return roomAgent{}, &r
}
