# AI assist

AI assists (docs/prd/2026-10-13-ai-assist.md) put one fast model call
behind every manual entry point: the composer's "enhance prompt", a form's
"Fill with AI". They are not agent turns: a turn of a member's agent takes
tens of seconds to start and needs a bridge online, while an assist answers
in seconds, in a browser, with no desktop.

`internal/assist` owns everything that touches the model; the gateway
(`services/gateway/assist.go`) admits the caller, resolves the context a
task declares and relays the reply. Nothing else calls a model.

## The assist model

- **One per deployment**, set by a super admin in the platform console
  (`GET/PUT /v1/platform/assist-model`, `POST …/test`): an
  OpenAI-compatible chat completions base URL (`…/v1`, where
  `/chat/completions` answers: a LiteLLM proxy, an OpenAI-compatible
  gateway), the model name and an API key.
- **The key** is sealed with `BNBLOC_SECRET_STORE_KEY` (`internal/sealbox`,
  the settings key `assist.model` as additional data) in platform
  settings, never returned (`keySet` only). Without the secret store key
  the model cannot be set and a stored one is not opened. An update
  without a key keeps the stored one; `clearKey` drops it; an empty base
  URL turns assists off.
- **The client** (`assist.Client`) reaches only the configured host
  (`extgov.NetGuard`: no link-local or metadata address either), never
  follows a redirect, and bounds a call to 60 s and 4000 reply tokens. A
  text task streams (`stream: true`, the usage in the last chunk; a proxy
  that answers one completion instead is read whole). A JSON task asks for
  `response_format: json_object` and again without it when the model
  answers 400.
- **An organization's own model** (`GET/PUT /v1/tenant/assist-model`,
  `POST …/test`, org admins): base URL, model and key, sealed under
  `assist.model/<tenant>` (so a row moved to another organization does
  not open). Its host must be one of the platform model's `orgHosts`
  (exact `host[:port]`, set by the super admin; none by default), so an
  org admin cannot point the gateway at an arbitrary address. A member
  runs on their organization's model while its host stays allowed, else
  on the platform's (`Settings.Effective`); the audit row says which. The
  platform's PUT replaces `orgHosts` with what it sends.
- **An organization may turn assists off** (`assistDisabled` in its
  settings, `PUT /v1/tenant/settings`), its own model or not. `GET /v1/assist` answers
  `available: false` (and no tasks) without a model or in such an
  organization; the web hides every assist entry then. There is no second
  path through members' agents.

## Tasks are content

- **The `assist` kind** (`bnbloc.assist.package/v1`, `contentpkg/assist.go`):
  `INSTRUCTIONS.md` is the task's system prompt (at most 16 KiB: every call
  sends it); `component.json` names its `output` (`text` or `json`), for
  JSON the `fields` its reply holds (`{name, description, type, item}`:
  `type` is `text`, `list` — strings — or `items`, a list of objects whose
  text fields `item` names), the `context` it may receive (`room`,
  `artifact`, `attachments`, `library`, `clock`, `capabilities`,
  `conversation`) and an
  optional `gate` (`{question, threshold}`: one line of at most 500
  characters, a probability above 0 and at most 1; below). Nothing
  else: no `requires`, no other files. An assist is no content use
  (`KindSpec.Use`): a room or member never turns one on, and it never
  reaches an agent's workspace.
- **Resolution by name, nearest first** (`assist.Tasks`): the member's own,
  a copy shared with them, their organization's, the platform's (the
  default content). An organization restyles `enhance-prompt` by publishing
  its own of that name; a member by saving one. The Expert builder drafts
  assists like any other kind. **The consent exception**
  (`assist.ConsentScope`, a contract's `consent`): `explain-source-use`
  explains a grant only an organization admin decides, so it resolves to
  the organization's version, else the platform's, never a member's own or
  a shared copy, in a run, in the status (`GET /v1/assist`) and when the
  gateway runs it itself (`runConsentJSON`).
- **Output contracts** (`wire.AssistContracts`,
  docs/prd/2026-10-20-no-built-in-content.md §2.3): the platform ships no
  assist prompt, only the 13 task names its web and gateway run
  (`wire.AssistTask*`, generated into `wire.gen.ts`) and what each must
  answer: its `output`, the reply fields the caller reads with their types
  and item fields, whether it must have a gate (a caller runs it gated) and
  the closed `values` the caller acts on (find-a-way's causes and actions,
  describe-room's dependency and finding kinds, describe-work's
  categories, draft-schedule's kinds). A package saved or installed under
  one of those names that does not meet its contract is refused
  (`contentpkg.CheckAssistContract`, on `Options.Contracts`: a save, an
  import, a platform install; reading stored content never), so a
  replaced prompt may change the wording, never break the dialog. More
  fields than the contract's are allowed; any other name is free. The
  contracts' field shapes are held to the forms they fill
  (`internal/contentpkg/assist_contracts_test.go`), and the web's closed
  lists to their values (web tests over `AssistContracts`).
  `bnbloc content contracts` prints them.
- **The default assists** (the `bnbloc-default-content` repository,
  published at platform scope; none ships in the binaries): `enhance-prompt`
  (text; room, attachments) rewrites the composer's text into a clear
  request; `describe-work` (json: `displayName`, `description`,
  `category`, `tags`; artifact) drafts a work's gallery entry, its
  categories the contract's values; `describe-room`
  (json: `displayName`, `description`, `category`, `tags`,
  `welcome`, `instructions`, `dependsOn` as `items` of `{what, kind,
  included, why}` and `findings` as `items` of `{field, quote, why}`;
  conversation) reads a room being saved as a component
  (docs/prd/2026-10-20-complete-room-capture.md): from the room's
  conversation and the setup being saved (the web sends it as `<text>`:
  the setup with the apps made in the room, its files with their sizes
  and whether they are included and the tools its runs had, the current
  values, and every text the package would carry under `package`, the
  instructions included) it says what the room's way of working depends on
  (`dependsOn`, kinds `instructions`, `app`, `file`, `tool`, `schedule`,
  `agent`, `data`, `other`: the list `web/src/lib/roomCapture.ts` reads,
  the contract's values; `included` is `yes` when
  the package should carry it), drafts the room's instructions from what
  the conversation established and its Library entry, its fields but
  `dependsOn` and `findings` held to `contentpkg.Form`'s, and finds in the
  package's text what should not leave the room (a secret, an internal
  link, a person's name, private data: the kinds `web/src/lib/roomReview.ts`
  reads); the dialog runs it on "Fill with AI" and before a save. Its
  instructions have their own budget (900 tokens: one call per save, three
  jobs).
  `draft-expert`,
  `draft-skill-pack` and `draft-prompt-pack` (json; no context) fill the
  Library's form from a sentence (plus the form's current values the web
  appends), their fields and item fields held to `contentpkg.Form`'s by a
  test; `plan-room` (json; library) proposes a room's id, name,
  description, expert, skill packs and first message, naming only what the
  library lists; `draft-schedule` (json; clock) turns plain words into a
  recurrence (`wire.ScheduleRecurrence`'s fields plus `text` and a local
  `at`). `review-skill` (json: `findings`, `name`, `displayName`,
  `description`, `tags`; no context) reviews a local skill in its publish
  step from the folder's name, file list and SKILL.md the web sends;
  `fill-settings` (json: `values`, items of `key` and `value`, and `notes`;
  no context) maps a pasted setup note onto the fields of the one settings
  form ("Fill from a note"), the declaration and the note in the text, a
  secret only into a secret field and never shown back. `suggest-skill-dirs`
  (json: `add`, items of `path` and `why`, and `notes`; no context)
  proposes, from the folders a skills provider found where other tools keep
  skills (paths under the home, counts, one skill name each, never a file's
  content), which to add to the member's extra skill folders; only listed
  paths are kept.
  `explain-source-use` (json: `summary`, `uses` as a list, `mismatch`; no
  context) explains to an organization admin what a room's scheduled tasks
  would do with organization records it asks to read, from the ask's
  purpose and the room owner's tasks as `<text>` (built by the gateway,
  `explainSourceInput` in `room_sources.go`, which the console passes
  through), and flags a task beyond the stated purpose; a task that
  carries `before` changed after the grant, and the gateway runs it
  itself (`runConsentJSON`: the organization's or the default, never the
  grantee's copy; on the grantee's model and budget) to tell the admins
  what the change does
  (`docs/prd/2026-10-20-rooms-as-components.md` §2.3, §7).
  `find-a-way` (json: `cause`, `summary`, `fixes` as `items` of
  `{action, target, why}`; room, capabilities; gated, threshold 0.7)
  says what stopped an agent's reply and proposes up to three fixes of the
  web's closed list (`web/src/lib/deadEnd.ts`, held to the contract's
  values by a web test; `docs/prd/2026-10-20-never-a-dead-end.md`). Each
  stays within a token budget, and states what its contract and callers
  need, in the default content's own tests.
- **No assist installed for a task**: its button is hidden
  (`useAssistTask`) and the manual path works; the gateway's own run of
  `explain-source-use` is skipped, logged, and the change is still told
  to the admins without an explanation.

## Checking its work

docs/prd/2026-10-25-ai-checks-its-work.md. An answer is checked before a
person sees it, by code or by reality, never by the model.

- **Shape repair, every JSON task**: a reply that is not the task's
  object is answered once more (`completeJSONTask`): the reply as the
  assistant's turn, then `assist.ShapeProblem` naming the fields. Each
  call is an audit row; the second says "shape repair".
- **Revision**: `AssistRequest.revise` (and `ServiceAssistRequest.revise`)
  `{answer, problems}`: the previous answer and the caller's problems
  (1 to 8 lines of at most 500 characters, `wire.CheckRevise`) join the
  prompt after `<text>` as `<previous_answer>` and `<problems>`, fenced,
  with one fixed line (`reviseNote`; golden `revise.txt`). The caller runs
  the loop: `component.ServiceClient.AssistChecked(ctx, task, req, check,
  rounds)` (at most `MaxAssistRounds`, 3), which answers the last fields
  and the problems still left, never a failed answer as a passing one.
  The web's one loop is `runCheckedJSONAssist` (lib/assist.ts, over the
  pure `refineJSON` in lib/assistModel.ts); its check is the form's own
  rules. Draft with AI (expert, skill pack, prompt pack: `formProblem`,
  `promptPackProblem` over the drafted form) and Fill from a note (a value
  a field would not take, a key that is no setting) use it.
- **The pages context** (`pages`): `urls` (at most 3) are fetched by the
  gateway (`assist.FetchPages`, `PagesClient`: https on a public address
  only, resolved and dialled by address, redirects only on the page's
  host, 10 s, 2 MiB) and given as text (markup, scripts and navigation
  left out) in `<pages>`, 20,000 characters in all; a page that cannot be
  read says why. A service's task that declares `pages` gets them too.
  Where they may be fetched from is the platform's server egress
  (docs/architecture/server-egress.md).

## A service's own tasks

A service component that declares `assist` runs its own JSON tasks
(named `<service>-…`) for an organization through
`POST /v1/component-assist/{task}`, signed: the organization's version of
the task else the platform's, the organization's model, no context, the
service's rate per organization, an audit row with the service as actor
(docs/architecture/service-components.md "AI tasks";
`services/gateway/component_assist.go`). The run is one
`completeJSONTask`, like every JSON run.

## One run

`POST /v1/assist/{task}` with `wire.AssistRequest`: the text (at most
`wire.MaxAssistText` characters, 16,000), and the ids of what it refers to —
`roomId`, `channelId` (the composer's channel), `expertId` (the expert
picked for the message), `artifactId`, `attachmentIds` (at most
`wire.MaxAssistAttachments`), `replySeq` (an agent reply in the
channel: the room context then holds that turn), `hasOwnAgent` (the
client's word for the capabilities context) and `gated`. It spends the
cost budget and the member's assist rate (20 a minute per replica share).

- **Context** is resolved only for what the task declares, as the member
  reads it: the room (the room service answers its name to members only;
  its expert — the one picked, else the room's — and the room's and the
  member's skill packs; the channel's last 10 messages through realtime's
  history and `roomlog.DecodeRow`), a deliverable of it (title, type by
  `contentpkg.WorkTypeOf`, file name, and the request that produced its
  current version through the recipe owner `deliverableRecipe`), and
  attachments of it (names and types), the library (`assist.LibraryOf`:
  the experts and skill packs the member may use, one per name, the
  nearest, their own first, at most 40 each, cut to a menu), the clock
  (now in the request's `timezone`, `schedule.LoadZone`'s rule, UTC when
  unknown) and the capabilities (`services/gateway/assist_capabilities.go`):
  the executable components the member catalog lists to the member
  (`memberComponents.listed`, the rule `GET /v1/components` lists by:
  name, kind, description, installed or not; at most 40), the
  organization's platform agents (shareagent's `GET /v1/platform-agents`,
  `core/provider`), and with a room its agents (realtime's roster,
  `roomRoster`, by handle) and how many other people it has (the room
  service's members, agent hosts left out), plus `hasOwnAgent`; a list
  past its bound says how many it left out. The conversation
  (`services/gateway/assist_conversation.go`, with a room): the room's
  name and its latest messages across its channels as the member reads
  them: the default channel and up to 11 more the room service lists
  (never the people chat), each channel's latest 80 rows through
  `roomHistory` and `roomlog.DecodeRow`, merged by room sequence; the
  newest are kept within `assist.NewConversation`'s budget (80 lines,
  each cut to 1,500 characters, 20,000 together) and shown oldest first,
  saying how many older ones were left out. The room's recent messages
  are the channel's latest 10, oldest first; `replySeq` adds the turn:
  that reply's row and the request its payload's `sourceSeq` names
  (`assistTurn`, both through `roomlog.DecodeRow`; a sequence that is not
  an agent's reply is a 404), each cut to 4000 characters. Anything the
  member cannot read is a 404; a deliverable or attachments without
  `roomId` a 400; neither text nor context a 400.
- **The prompt** (`assist.Messages`): the instructions as the system
  message, then one user message introducing the blocks as data, with
  `<room>`, `<recent_messages>`, `<turn>`, `<artifact>`, `<attachments>`,
  `<library>`, `<clock>`, `<capabilities>`, `<conversation>` and the member's `<text>`; the block tags inside untrusted text are neutralized,
  so it cannot close its block or open another. Goldens pin the layout
  (`internal/assist/testdata/golden`).
- **A text task** answers Server-Sent Events: `delta` `{text}` per piece,
  then `done` `{usage}`; a failure after the first piece is an `error`
  `{error}` event, before it an HTTP 502.
- **A JSON task** answers `wire.AssistJSONReply`: the declared fields only,
  in their shapes (a text field a string; a list field a list of strings,
  a string split at commas; an items field a list of objects holding only
  the item fields as strings, an item with none dropped, a plain string
  taken for a one-field item, at most `wire.MaxAssistItems`), from the
  reply's object (a fenced block tolerated); a reply that is not that
  object is a 502. The content validator still owns what a form saves.
- **The gate** (`services/gateway/assist_gate.go`): a run with `gated`
  of a task that declares a `gate` first asks the decision model its
  question, with the run's user message (the blocks above, fenced and
  introduced as data, cut to `wire.MaxDecisionInputText`) as the input,
  through the gateway's one decision call (`callDecision`: the caller's
  provider, the call, its audit row). Below the threshold, or a declined
  question, it answers `wire.AssistSkipped` `{skipped: true, reason:
  "below"}`; with no decision model for the caller `{reason: "no-gate"}`;
  in both no chat model runs. A decision failure is the decision route's
  answer (its status, or 502). A gated run of a task without a gate is a
  400. The platform names no question: the gate is the task's.
- **Audit**: every run is an audit row of kind `assist` (`task@version`,
  the component, the model, the token counts and the latency, the failure
  class), never the text; a model change is one too.

## Find a way (a room's card)

`docs/prd/2026-10-20-never-a-dead-end.md`. `web/src/lib/deadEnd.ts` owns
the closed action list (`install`, `ask-agent`, `attach`, `own-agent`,
`rephrase`, `report`), reads a `find-a-way` reply against what the member
can act on (an install target must be listed and not installed, an agent
one of the room's other than the one that replied; anything else is
dropped, at most three fixes), the fallback (attach, ask another agent,
rephrase) and the once-per-reply memo. `views/room/useDeadEnd.ts` runs it:
gated when a turn the member asked for themselves reaches `replied` live
in the channel they look at (never one an agent dispatched), ungated from
an agent reply's "Didn't work? Find a way"; without an assist model the
menu shows the fallback. The card (`views/room/DeadEndCard.tsx`) is the
member's local state, never posted; each fix runs an existing flow on a
click: `install` the member catalog's install after a confirmation (and
the component's settings when it needs any), `ask-agent` the request
resent to that agent in the room, `attach` the composer's file picker
(`composerFilePicks`), `own-agent` their own agent picked with the request
in the composer (or the Agents page: the desktop app and its local
agents), `rephrase` the request through `enhance-prompt` into the
composer, `report` a note the member reviews, sent to the organization's
admins (`POST /v1/rooms/{roomId}/report`, notification `room_report`).
