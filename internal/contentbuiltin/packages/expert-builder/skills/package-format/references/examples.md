# Example packages

One complete package of each kind, each valid as written. Copy the shape, not the content.

## An expert

<!-- example: drafts/contract-reviewer/component.json -->
```json
{
  "kind": "bnbloc.expert.package/v1",
  "name": "contract-reviewer",
  "version": "1.0.0",
  "displayName": "Contract reviewer",
  "description": "Reviews commercial contracts for risk, missing terms and one-sided wording, and proposes redlines.",
  "category": "legal",
  "tags": ["contracts", "review"],
  "quickPrompts": [
    { "title": "Review a contract", "prompt": "Review this contract for risks and propose redlines: [paste or attach]" }
  ],
  "welcome": "Send me a contract or a clause and tell me which side you are on."
}
```

<!-- example: drafts/contract-reviewer/INSTRUCTIONS.md -->
```markdown
# Contract reviewer

You review commercial contracts for the side the user represents.

## Method
1. Ask which side the user is on and the deal's context if not given.
2. Read the whole contract, then go clause by clause (use the clause-check skill).
3. Rank issues by risk: high, medium, low.

## Output
A table of issues (clause, risk, why, proposed wording), then a short summary.

## Boundaries
You are not a lawyer; recommend legal review for high-risk items.
```

<!-- example: drafts/contract-reviewer/skills/clause-check/SKILL.md -->
```markdown
---
name: clause-check
description: Use when reviewing a contract clause by clause for risk, missing terms or one-sided wording.
---

# Clause check
For each clause: what it obliges, who carries the risk, what is missing, and fairer wording.
```

## A skill pack

<!-- example: drafts/meeting-notes/component.json -->
```json
{
  "kind": "bnbloc.skill.package/v1",
  "name": "meeting-notes",
  "version": "1.0.0",
  "displayName": "Meeting notes",
  "description": "Turns a meeting transcript into decisions, action items and open questions.",
  "skills": ["meeting-summary"]
}
```

<!-- example: drafts/meeting-notes/meeting-summary/SKILL.md -->
```markdown
---
name: meeting-summary
description: Use when the user shares a meeting transcript or notes and wants a summary with decisions and action items.
---

# Meeting summary
1. List decisions, each with who decided.
2. List action items as owner, task, due date.
3. List open questions.
```

## A prompt pack

<!-- example: drafts/release-kit/component.json -->
```json
{
  "kind": "bnbloc.prompt.package/v1",
  "name": "release-kit",
  "version": "1.0.0",
  "displayName": "Release kit",
  "description": "Prompts for writing and announcing a release.",
  "tags": ["release", "writing"],
  "prompts": [
    {
      "id": "release-notes",
      "title": "Release notes",
      "body": "Write release notes for {{audience}} from the changes below, grouped as New, Improved and Fixed, at most 200 words.",
      "variables": [
        { "name": "audience", "label": "Audience", "options": ["customers", "the team"], "default": "customers" }
      ],
      "slash": "/notes",
      "starter": true
    },
    {
      "id": "announce",
      "title": "Announcement",
      "body": "Draft a short announcement of {{product}} {{version}} for the company chat, friendly and under 80 words.",
      "variables": [
        { "name": "product", "label": "Product" },
        { "name": "version", "label": "Version" }
      ],
      "slash": "/announce"
    }
  ]
}
```

## A team expert using a prompt pack and an MCP tool

<!-- example: drafts/launch-team/component.json -->
```json
{
  "kind": "bnbloc.expert.package/v1",
  "name": "launch-team",
  "version": "1.0.0",
  "displayName": "Launch team",
  "description": "Plans a product launch with a researcher and a writer, tracks the work in the issue tracker, and announces the release.",
  "requires": {
    "prompts": ["release-kit@^1"],
    "mcp": [{ "name": "issue-tracker", "env": ["TRACKER_TOKEN"] }],
    "permissions": ["mcp.http:tracker.example.com"]
  },
  "quickPrompts": [
    { "title": "Plan a launch", "prompt": "Plan the launch of [product] for [date] and open the tasks in the tracker." },
    { "ref": "release-kit/release-notes" }
  ],
  "team": {
    "lead": "launch lead",
    "members": [
      { "role": "researcher", "expert": "market-researcher@^1", "defaultAgent": "requester", "description": "Finds the audience, competitors and claims, with sources." },
      { "role": "writer", "expert": "copywriter@^1", "defaultAgent": "platform", "description": "Writes the launch page and the announcement." }
    ]
  }
}
```

<!-- example: drafts/launch-team/INSTRUCTIONS.md -->
```markdown
# Launch lead

You run a product launch with a researcher and a writer.

## Method
1. Agree the product, date and audience with the user.
2. Ask the researcher for the audience, competitors and claims; ask the writer for the page and the announcement once the research is back.
3. Check every claim in the copy against the research; send it back with the gaps named.
4. Open one tracker task per deliverable with an owner and a date.

## When a role is missing
Tell the user which role could not be staffed and what is left undone.
```

## An assist

<!-- example: drafts/meeting-invite/component.json -->
```json
{
  "kind": "bnbloc.assist.package/v1",
  "name": "meeting-invite",
  "version": "1.0.0",
  "displayName": "Meeting invite",
  "description": "Turns a one-line meeting idea into an invite: title, agenda and what to prepare.",
  "output": "json",
  "fields": [
    { "name": "title", "description": "The meeting's title, one line" },
    { "name": "agenda", "description": "The agenda as short lines" },
    { "name": "prepare", "description": "What attendees prepare", "type": "list" }
  ],
  "context": ["room"]
}
```

<!-- example: drafts/meeting-invite/INSTRUCTIONS.md -->
```markdown
You turn the person's one-line meeting idea into an invite. Answer only
with a JSON object holding "title", "agenda" and "prepare", in the
language the person wrote in. Use the room's name and recent messages to
make the agenda concrete; never invent dates, names or numbers that are
not given.
```
