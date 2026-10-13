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
- **An organization may turn assists off** (`assistDisabled` in its
  settings, `PUT /v1/tenant/settings`). `GET /v1/assist` answers
  `available: false` (and no tasks) without a model or in such an
  organization; the web hides every assist entry then. There is no second
  path through members' agents.

## Tasks are content

- **The `assist` kind** (`bnbloc.assist.package/v1`, `contentpkg/assist.go`):
  `INSTRUCTIONS.md` is the task's system prompt (at most 16 KiB: every call
  sends it); `component.json` names its `output` (`text` or `json`), for
  JSON the `fields` its reply holds (`{name, description, list}`), and the
  `context` it may receive (`room`, `artifact`, `attachments`). Nothing
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
  categories held to `wire.WorkCategories` by a test. Each stays within a
  token budget (`assists_test.go`).

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
  attachments of it (names and types). Anything the member cannot read is
  a 404; a deliverable or attachments without `roomId` a 400; neither text
  nor context a 400.
- **The prompt** (`assist.Messages`): the instructions as the system
  message, then one user message introducing the blocks as data, with
  `<room>`, `<recent_messages>`, `<artifact>`, `<attachments>` and the
  member's `<text>`; the block tags inside untrusted text are neutralized,
  so it cannot close its block or open another. Goldens pin the layout
  (`internal/assist/testdata/golden`).
- **A text task** answers Server-Sent Events: `delta` `{text}` per piece,
  then `done` `{usage}`; a failure after the first piece is an `error`
  `{error}` event, before it an HTTP 502.
- **A JSON task** answers `wire.AssistJSONReply`: the declared fields only
  (strings; a list field a list of strings, a string split at commas),
  from the reply's object (a fenced block tolerated); a reply that is not
  that object is a 502.
- **Audit**: every run is an audit row of kind `assist` (`task@version`,
  the component, the model, the token counts and the latency, the failure
  class), never the text; a model change is one too.
