# Model display names travel with the model (2026-10-29)

Status: **design, before code.** Owner: the platform (Core Protocol, the bridge's
advertised catalog, the web composer) and the ACP core. Reviewed against
`docs/design-review.md` (one rule one owner; the platform names no model or
provider; additive public contracts).

## 1. What the owner saw

The composer's model picker listed an agent's models as "Acme Anthropic/x Claude
Haiku 5.5" and "Acme/x GPT 6 Luna". The ids are `acme-anthropic/x-claude-haiku-5-5`
and `acme/x-gpt-6-luna`: a provider prefix the agent's own configuration chose,
and a trial-channel `x-`. The label is wrong because the web **guesses** it from the id
(`prettyModel`: a dash between digits is a version dot, words are capitalised), and an id
that carries a provider and a channel marker cannot be read that way.

## 2. Root cause: the name is dropped four times

An ACP agent reports each model with a value **and a display name** in its
`session/new` answer (checked on the real agents, 2026-10-29):

| agent | value | name the agent gives |
|---|---|---|
| pi | `anthropic/claude-haiku-4-5` | `anthropic/Claude Haiku 4.5 (latest)` |
| kilo | `kilo/aion-labs/aion-3.5` | `Kilo Gateway/AionLabs: Aion 3.5` |
| omp | `litellm/amazon/titan-embed-text-v2` | `amazon/titan-embed-text-v2` |

and then:

1. the ACP core's `acpConfigOption.values()` keeps only `value`;
2. Core Protocol's `Target.Models` is `[]string`;
3. the bridge advertises `modelOptions.models` as strings (`heartbeatAgents`);
4. the web renders `prettyModel(id)`.

So the display name exists at the source and is thrown away at every hop, and the last
hop replaces it with a heuristic that the platform has no business owning.

## 2b. The dropdown itself (found the same day, fixed first)

The panel was anchored to the picker's left edge above a narrow breakpoint, but the picker sits
at the right end of the composer's row, so on a 520px window the 240px panel ran 167px off the
screen, and every row wrapped. It is now anchored to the picker's right edge, sized by its
content (`w-max`, at least 15rem, at most the viewport), and rows never truncate: a name is shown
whole. Measured in a real browser at 375 / 520 / 744 / 1200 / 1920px: the panel's right edge is
inside the viewport and no row is truncated.

## 3. Design (the end state)

A model is `{value, name?}`. The name is **optional**: an agent or core that gives none is
unchanged and the web falls back to `prettyModel`, so every existing core keeps working.

- **Core Protocol v1, additive:** `Target.ModelNames map[string]string` (value → name),
  omitted when empty. A map beside the unchanged `Models []string` (not a change of its
  element type) so a client that does not know it reads exactly what it read before.
- **ACP core:** `values()` also returns each option's `name`; the catalog keeps them
  (`agentStatus.ModelNames`) and `targets()` fills `Target.ModelNames`. A declared list
  (config `models`) may carry names: `"models"` stays a string list and a new
  `"modelNames": {"<value>": "<name>"}` supplies them (additive config field).
- **Bridge:** `core.Target.ModelNames` (neutral), `modelOptions.modelNames` in the heartbeat.
- **Gateway:** the room catalogs store the advertised options as JSON and decode them into
  `wire.AgentModelOptions` (one new field carries the names); `GET /v1/local-agent/targets`
  rebuilds the options field by field and had to be taught the new one (found by a test that
  failed when it did not carry them, not assumed).
- **Web:** `ModelCatalog.modelNames`; the picker shows `names[m] ?? prettyModel(m)`. No
  model, provider or channel is named in platform code.
- **A core's own declarations** (a sidecar that declares models for agents the ACP core cannot
  ask) may carry names the same way, `models` plus `modelNames`; the **name is built from
  the menu's own model name**, with nothing agent-specific (the value already carries the
  provider the agent's selector needs; the label is the model, e.g. "x-claude-haiku-5-5
  (Claude)"): one rule in one place, `ModelLabel(m SharedModel)`.

Not done here, and why: translating a model id into a nicer name for every agent in the
web. That is a per-model fact; it belongs to whoever knows the model (the agent, the recipe,
the account's menu), never to the platform.

## 4. Compatibility

- Old ACP core + new bridge/web: no names; `prettyModel` fallback (today's behaviour).
- New ACP core + old bridge: the field is ignored (an older Core Protocol client decodes
  `Target` leniently; verify with the conformance suite).
- New web + old gateway: `modelNames` absent; fallback.
- A gateway that stores `modelOptions` as typed columns: checked in the plan (a migration
  would make this a bigger change, which the plan must say before it is built).

## 5. The same record: an agent in the sandbox that no recipe declares

`opencode2` (the original agent, on its free models) has **no model picker** at all before
its first session, and none of this fixes that: nothing declares its models, and the core cannot
probe a guest agent between sessions because it holds no sandbox descriptor then. The only
mechanism that works for every guest agent, with no per-agent knowledge, is to **learn the list
from a real session and keep it** (the core already does, in memory: `observe`), and to make it
survive a restart and show up before the member's first turn:

- keep the last observed catalog of a guest agent in the core's state (a file, per agent, with
  its observation time), serve it as the target's models until a newer one is observed;
- the first ever session still shows an empty picker (honest: nothing is known), and says
  "choose after the first message" rather than nothing. The alternative, probing in the sandbox
  with a descriptor the core does not hold between turns, needs the bridge to hand the core a
  standing token for a throwaway space: a new authority, with the exec token's whole risk, for a
  cosmetic gain. **Not proposed.** The owner decides.

## 6. Plan, each step green before the next

1. Core Protocol `Target.ModelNames` + conformance case (a core may omit it; a client ignores an
   unknown field); wire regenerated.
2. ACP core keeps names; config `modelNames`; tests against the real agents' answers
   (fixtures captured above).
3. Bridge: neutral Target, heartbeat; gateway `AgentModelOptions.ModelNames`.
4. Web: catalog, picker, `restrictCatalog`/borrow grants keep names; node tests.
5. A sidecar that declares models declares their names too (its own repository).
6. ACP core: persist the last observed guest catalog (section 5, first bullet).

## 7. Pre-mortem

"A month on, a model shows a wrong or odd name." (a) A name an agent reports is itself odd
(`Kilo Gateway/AionLabs: Aion 3.5`): shown as the agent gives it; the platform does not rewrite
it. (b) A borrower sees the owner's names: names are display only; the value is what is sent and
authorised (`allowedModels` stay values). (c) Two models with one name: the picker shows the
value as a tooltip. (d) The name carries markup or a very long string: truncated and rendered as
text, never as HTML.
