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
  text fields `item` names), and the `context` it may receive (`room`,
  `artifact`, `attachments`, `library`, `clock`). Nothing
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
  (json: `displayName`, `description`, `category`, `tags`, `welcome`;
  room) drafts a room component's Library entry in the "Save as
  component" dialog from the room and the setup being saved (the web sends
  it as `<text>`), its fields held to `contentpkg.Form`'s. `draft-expert`,
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
  paths are kept. Each stays within a token budget (`assists_test.go`).

## One run

`POST /v1/assist/{task}` with `wire.AssistRequest`: the text (at most
`wire.MaxAssistText` characters), and the ids of what it refers to —
`roomId`, `channelId` (the composer's channel), `expertId` (the expert
picked for the message), `artifactId`, `attachmentIds` (at most
`wire.MaxAssistAttachments`). It spends the cost budget and the member's
assist rate (20 a minute per replica share).

- **Context** is resolved only for what the task declares, as the member
  reads it: the room (the room service answers its name to members only;
  its expert — the one picked, else the room's — and the room's and the
  member's skill packs; the channel's last 10 messages through realtime's
  history and `roomlog.DecodeRow`), a deliverable of it (title, type by
  `contentpkg.WorkTypeOf`, file name, and the request that produced its
  current version through the recipe owner `deliverableRecipe`), and
  attachments of it (names and types), the library (`assist.LibraryOf`:
  the experts and skill packs the member may use, one per name, the
  nearest, their own first, at most 40 each, cut to a menu) and the clock
  (now in the request's `timezone`, `schedule.LoadZone`'s rule, UTC when
  unknown). Anything the member cannot read is
  a 404; a deliverable or attachments without `roomId` a 400; neither text
  nor context a 400.
- **The prompt** (`assist.Messages`): the instructions as the system
  message, then one user message introducing the blocks as data, with
  `<room>`, `<recent_messages>`, `<artifact>`, `<attachments>`,
  `<library>`, `<clock>` and the member's `<text>`; the block tags inside untrusted text are neutralized,
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
- **Audit**: every run is an audit row of kind `assist` (`task@version`,
  the component, the model, the token counts and the latency, the failure
  class), never the text; a model change is one too.
