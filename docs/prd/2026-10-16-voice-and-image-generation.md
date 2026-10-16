# Voice input and image generation (2026-10-16)

Status: design accepted in review (2026-10-16); building M1 up.

Two model abilities the room product keeps asking for, both re-implemented
from scratch on the platform's own seams (nothing here imports or names
another product's code):

- **Voice input.** A member presses the microphone in the composer, speaks,
  and the words land in the input box — speech to text as one fast model
  call, like an assist: seconds, in a browser, no agent turn.
- **Image generation.** "Draw me a poster for Friday's demo" to your agent:
  today the agent has no way to draw. A `generate_image` room tool generates
  from a prompt and publishes the image into the room's Docs.

Both are **media calls** (audio in → text out; prompt in → image out), not
assists: no task content, no prompt fencing of a member's text, different
upstream APIs. They get their own owner (`internal/media`) beside
`internal/assist`, and their own provider settings.

## 2. Providers: the external API is config, not code

Providers differ per deployment and per capability, so nothing about a
vendor is hardcoded (AGENTS.md: no hardcoded environment data). What is
code is only the *wire protocol family*:

- **One provider config per capability** — transcribe, image — each one
  platform setting: `media.stt`, `media.image`, holding
  `{kind, baseUrl, model, sealedKey, params}`.
- **`kind` selects an adapter** from a registry in `internal/media`. The
  first adapter is `openai-compatible` (`POST {base}/audio/transcriptions`
  multipart; `POST {base}/images/generations` JSON, asking for
  `response_format: "b64_json"` and retrying without it when the provider
  answers 400 — the assist client's response_format rule). A new vendor
  with another wire shape is a new adapter file and a registry entry; no
  route, setting or UI change.
- **`params`** is a flat string map the adapter owns: extra form fields for
  a transcription (e.g. a language hint), extra JSON fields for an image
  (e.g. `size.square` / `size.landscape` / `size.portrait` presets).
  Bounded: at most 16 keys, 64-character keys, 512-character values. A new
  provider's knobs arrive as config, not schema changes.
- **The key is sealed** like the assist model's (`internal/sealbox`, the
  setting key as additional data), never returned, `keySet` only. "Test"
  makes one real short call per capability and reports the latency.
- **Host guard:** the same `extgov.NetGuard` rule as the assist client —
  only the configured host, no redirects.
- **The shared rules of "an admin-configured model endpoint"** (base URL /
  model / key validation) move to their one owner: a new
  `internal/modelsettings` package extracted from
  `internal/assist/settings.go`; `assist.Settings` delegates to it. Assist
  behavior and goldens unchanged (one rule, one owner).
- **An organization turns media off** with its existing `assistDisabled`
  switch: the same meaning (no data leaving to a model), the same setting.
  `GET /v1/media` answers per caller: `{transcribe: {available}, image:
  {available}}`.
- **Round 1 is platform-level only.** An organization's own providers
  (the assist org-model pattern: a per-tenant row, the platform's
  `orgHosts` allowlist) is M6.

## 3. Speech to text (voice input)

**Route.** `POST /v1/media/transcriptions`: the raw audio body
(`Content-Type: audio/*`), at most 20 MiB, a `filename` query parameter
optional. The adapter re-posts it as multipart to the provider; the reply's
`text` comes back capped at 32,768 characters (never splitting a UTF-16
surrogate pair). Drives `PermDriveAgent` (the assist routes' rule: it feeds
message composition), spends the cost budget and the member's assist rate
(20 a minute per replica share), and is audited kind `assist`, name
`transcribe` — the model, the latency, never the audio or the text.

**Web.** The composer gains a microphone button (the room's main composer,
members only), hidden when `GET /v1/media` says transcribe is unavailable
or the organization turned AI off:

- `lib/voice.ts` (one owner, plain Web Audio): `getUserMedia` +
  `MediaRecorder` (`audio/webm;codecs=opus`, `audio/mp4` fallback), decode
  with `decodeAudioData`, resample to 16 kHz mono PCM through an
  `OfflineAudioContext`, and encode a 44-byte-header RIFF/WAVE file. ~80
  lines of standard browser APIs, written here, no external code. A
  recording auto-stops at 120 s (~3.8 MiB as 16 kHz mono WAV, far under the
  route's cap).
- `useVoiceInput` (composer hook): idle → recording (a stop button and a
  timer) → encoding → transcribing (a spinner on the mic); the transcript
  inserts at the caret with a trailing space and never auto-sends; a
  failure is one plain sentence.

## 4. Image generation

**Route.** `POST /v1/media/image`: `{prompt (≤ 4000 chars), aspect:
square | landscape | portrait}` → `{dataUrl, mimeType, width, height,
revisedPrompt}`. The reply is bounded (16 MiB read), magic-byte sniffed
(PNG / JPEG / WebP only), and PNG dimensions are parsed best-effort. A
provider's content refusal maps to a 4xx carrying its message. Same
permission, budget, rate and audit as transcribe (name `image`, never the
prompt). The adapter asks for inline base64 only; a reply that names an
http(s) URL instead is refused (no second egress path).

**The room tool (M3).** `generate_image`: `{prompt, aspect?, fileName?}`.
The bridge calls the route with the job grant (`?job=&room=` — the content
routes' admission, acting as the requester), then hands the bytes to a new
`Env` seam the bridge owns (`publishArtifactNow`, the existing one owner of
"a file becomes a room artifact version"). The image lands in the room's
Docs attributed to the running job (`sourceJobId` set, so its recipe is
captured and "Make similar" works), and **the image never passes through
the model's context** — the tool result is the artifact id, the file name
and a one-line note. `revisedPrompt` is fenced as provider data. The
brief (tools/list) stays one line; `brief.go` and the `tools_list.json`
golden are updated with it.

**The transcribe tool (M4).** A room tool reading a workspace audio file
(an attachment the member posted) through the same route, returning fenced
text — for agents whose model cannot hear.

## 5. The app SDK (M5)

`app.ai` gains `app.ai.image({prompt, aspect})` (→ `{dataUrl, mimeType,
width, height}`) and `app.ai.voice(blob)` (→ `{text}`): the same routes,
the caller's own rate, signed-in callers only, the organization's switch
obeyed. Demo mode answers in memory with a refusal-shaped stub (a demo
page never reaches a model). The interactive-apps skill's SDK block gains
the two calls.

## 6. An organization's own providers (M6)

The assist org-model pattern: a per-tenant setting row
(`media.stt/<tenant>`, `media.image/<tenant>`), the platform row carrying
the `orgHosts` allowlist. Not in round 1.

## 7. Design review

| Lens | Answer |
|---|---|
| Actors | A member dictates (their own composer, their rate); an agent generates as the requester of the job it answers (contentGrant admission, the requester's budget). An org admin turns media off (`assistDisabled`); a super admin sets providers; a visitor or an agent session gets nothing (agents reach media only through their room tools). |
| Time and lifecycle | A call is stateless; a provider config change applies to the next call (settings are read per call, like the assist model's). A recording is capped at 120 s client-side and 20 MiB server-side; no persisted state anywhere. |
| Scale and cost | Calls share the member's assist rate (20/min) and the cost budget; every call is audited with the model and latency. A generated image is one room artifact version under the room's own caps. Reply bytes bounded (16 MiB image, 32 KiB transcript). |
| Failure and recovery | Provider unreachable → one plain 502 sentence; a content refusal → 4xx with the provider's message; a reply that is not the expected shape → 502 (`unreadable reply`). No retries that repeat a side effect: the only write is the M3 artifact publish, and a re-run makes a new version, like any re-run deliverable. |
| Boundaries and privacy | Keys sealed per setting key, never returned. Audio, prompts, transcripts and images are never audited or logged (only the model name, latency and failure class). The page-side SDK routes stay under the app capability; the room tool acts as the requester through the job grant. |
| Abuse and untrusted input | Rate + budget per member; the provider's own moderation is the authority for what it will draw (we do not build a second classifier — the owner of the rule is the provider). `revisedPrompt` and transcripts are fenced as provider/user data wherever an agent reads them. The web hides both entries when the capability is off. |
| One owner | `internal/media` owns every provider call (nothing else calls a media API); `internal/modelsettings` owns the shared endpoint-setting rules (extracted from assist); `lib/voice.ts` owns recording/encoding; `lib/media.ts` owns the web routes (a new boundary rule beside B2); the bridge's publish path owns artifact publication. |
| Defaults and fallbacks | No provider → the routes answer a clear 409 sentence and the web hides the buttons; the tool result says the deployment has no image model. `params` absent → adapter defaults (sizes 1024², 1536×1024, 1024×1536). An org without its own provider uses the platform's (M6). |
| External contracts | The OpenAI-compatible shapes are verified per adapter with in-repo fakes pinning the wire (multipart fields, `b64_json`, error bodies); a real proxy is exercised by the admin "Test" button at configure time, not assumed. |
| Load on users and agents | One new room tool in M3 with a one-line brief; M4 adds one more. No session guidance changes; the composer gains one button. The SDK grows two calls documented in the skill. |
| Release and compatibility | Additive routes, settings and SDK methods; pre-launch, no migrations. The room tool appears with the bridge deploy; `tools_list.json` and the brief test are updated in the same change. |
| Observability | Every call audited (kind `assist`, name `transcribe`/`image`, model, latency, failure class); provider failures surface as `obs.ServerError` with the class; settings changes audited like a model change. |

**Pre-mortem.**

1. **"The mic button does nothing for some members."** An org without a
   provider, or with AI off: `GET /v1/media` is the one status the web
   reads; the button hides; a direct call answers 409 with a sentence.
2. **"The generated image never reached Docs."** The M3 seam is the
   bridge's existing publish path, tested end to end against a fake room
   service (artifact created, version published, `sourceJobId` carried).
3. **"The agent burned the budget on twenty pictures."** One image per
   call, the member's rate and budget, and a brief that says to confirm
   with the person what to make.
4. **"Audio or a prompt leaked into a log."** The audit row and the slog
   fields are named in the tests; a test asserts the audit detail contains
   no prompt, transcript or audio bytes.
5. **"A provider reply pointed us at another host."** Inline base64 only;
   a URL reply is refused; the only egress is the guarded client to the
   configured host.
6. **"A second vendor arrived and the settings could not describe it."**
   Its wire family is a new adapter; its address, model, key and knobs are
   config (`kind`, `baseUrl`, `model`, `params`) — no route or schema
   change.

## 8. Milestones

- **M1:** `internal/modelsettings` extraction; `internal/media` (adapter
  registry, the openai-compatible adapter, settings `media.stt` /
  `media.image`, routes `GET /v1/media`, `POST /v1/media/transcriptions`,
  `POST /v1/media/image`); the platform console's "Media models" card with
  Test buttons; tests; `docs/architecture/media-models.md`.
- **M2:** the composer's voice input (`lib/voice.ts`, `lib/media.ts`,
  `useVoiceInput`, the mic button).
- **M3:** the `generate_image` room tool and the bridge publish seam.
- **M4:** the transcribe room tool.
- **M5:** `app.ai.image` / `app.ai.voice` in the app SDK + the skill note.
- **M6:** organization-level providers.

Each milestone lands with its red check (a test that fails with the change
reverted): the WAV encoder's golden bytes, the routes' fake-provider
happy/refusal/oversize cases, the audit row's content, the tool's publish
path, the boundary rule's sample, the tools/list golden.
