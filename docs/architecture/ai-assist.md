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
  follows a redirect, and bounds a call to 60 s and 2000 reply tokens. A
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
  `artifact`, `attachments`, `library`, `clock`, `capabilities`) and an
  optional `gate` (`{question, threshold}`: one line of at most 500
  characters, a probability above 0 and at most 1; below). Nothing
  else: no `requires`, no other files. An assist is no content use
  (`KindSpec.Use`): a room or member never turns one on, and it never
  reaches an agent's workspace.
- **Resolution by name, nearest first** (`assist.Tasks`): the member's own,
  a copy shared with them, their organization's, the platform's, the
  built-in. An organization restyles `enhance-prompt` by publishing its own
  of that name; a member by saving one. The Expert builder drafts assists
  like any other kind.
- **Built-ins** (`internal/contentbuiltin/packages`): `enhance-prompt`
  (text; room, attachments) rewrites the composer's text into a clear
  request; `describe-work` (json: `displayName`, `description`,
  `category`, `tags`; artifact) drafts a work's gallery entry, its
  categories held to `wire.WorkCategories` by a test; `describe-room`
  (json: `displayName`, `description`, `category`, `tags`, `welcome`,
  and `findings` as `items` of `{field, quote, why}`; room) drafts a room
  component's Library entry in the "Save as component" dialog from the
  room and the setup being saved (the web sends it as `<text>`, with every
  text the package would carry under `package`), its fields but
  `findings` held to `contentpkg.Form`'s, and finds in that text what
  should not leave the room (a secret, an internal link, a person's name,
  private data: the kinds `web/src/lib/roomReview.ts` reads, held to the
  item's description by a web test); the dialog runs it before a save.
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
  carries `before` changed after the grant, and the gateway runs the
  built-in itself (`runBuiltinJSON`, on the grantee's model and budget) to
  tell the admins what the change does
  (`docs/prd/2026-10-20-rooms-as-components.md` §2.3, §7).
  `find-a-way` (json: `cause`, `summary`, `fixes` as `items` of
  `{action, target, why}`; room, capabilities; gated, threshold 0.7)
  says what stopped an agent's reply and proposes up to three fixes of the
  web's closed list (`web/src/lib/deadEnd.ts`, which a web test holds to
  the package's field descriptions;
  `docs/prd/2026-10-20-never-a-dead-end.md`). Each stays within a token
  budget (`assists_test.go`).

## One run

`POST /v1/assist/{task}` with `wire.AssistRequest`: the text (at most
`wire.MaxAssistText` characters), and the ids of what it refers to —
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
  past its bound says how many it left out. The room's recent messages
  are the channel's latest 10, oldest first; `replySeq` adds the turn:
  that reply's row and the request its payload's `sourceSeq` names
  (`assistTurn`, both through `roomlog.DecodeRow`; a sequence that is not
  an agent's reply is a 404), each cut to 4000 characters. Anything the
  member cannot read is a 404; a deliverable or attachments without
  `roomId` a 400; neither text nor context a 400.
- **The prompt** (`assist.Messages`): the instructions as the system
  message, then one user message introducing the blocks as data, with
  `<room>`, `<recent_messages>`, `<turn>`, `<artifact>`, `<attachments>`,
  `<library>`, `<clock>`, `<capabilities>` and the member's `<text>`; the block tags inside untrusted text are neutralized,
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
