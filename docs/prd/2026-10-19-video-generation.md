# Video in the room: the agent films (2026-10-19)

Status: slice 1 (text to video): the platform side is built (§4 step 1:
the capability, the routes, the report, the console cards, the relay
routes and timeout); media-tools' `generate_video` and the QA provider
are next. Owner: media-tools component + the platform's media capability.

A member asks the room's agent for a short video ("a 6-second clip of a
paper boat on a pond at sunrise") and it appears in the room's files, like
a picture the agent draws today
(`docs/prd/2026-10-17-image-generation-redesign.md`).

## 1. Why

- Members already ask agents for pictures; a clip for a deck, a product
  demo or a social post is the next ask, and the organization's model
  gateway now serves a video model (a Veo model behind an
  OpenAI-compatible videos API, verified 2026-10-19: a 4-second 720p clip
  in about 30 seconds, 1.45 MB MP4).
- A video takes from half a minute to a few minutes. Nothing in the media
  path today waits that long: the provider call is synchronous, and the
  room relay cuts any call at 20 seconds.

## 2. What it is (the end state)

### 2.1 A video capability beside the image one

- `internal/media` gains the capability `video`, configured like `image`
  (a provider per platform or organization: kind, base URL, models, key,
  org hosts, params; the console's "Video generation" card; the media
  report's `video.available` and `video.models`).
- The `openai-compatible` adapter serves it with the OpenAI videos API:
  `POST {base}/videos` (model, prompt, seconds, size) answers a video id;
  `GET {base}/videos/{id}` its status; `GET {base}/videos/{id}/content`
  the MP4. No new adapter kind.
- AI off for the organization, no provider, or the cost budget spent:
  the same refusals as an image.
- The console's Test reads the provider's model list (`GET
  {base}/models`) and says whether it names the default model: filming a
  test clip would bill a video.

### 2.2 Asynchronous, and the gateway keeps nothing

- `POST /v1/media/video` (drive permission, the cost budget, the assist
  rate limit) submits the video and answers at once: `{id, status}`. The
  id is the provider's own (it names the provider's operation), so any
  gateway replica can answer for it; the gateway stores no job.
- `GET /v1/media/video/{id}` answers its status. Once it is done, with
  `publish` and the job's room (the relay adds them), the gateway reads
  the MP4 (at most the room's file cap) and publishes it as a room file
  (the image path's publish, generalized to a media file:
  `publishMediaFile`), and answers the artifact. A done video is
  published once: the artifact's id is derived from the room and the
  video id (`videoArtifactID`), so a repeated poll finds the file and
  answers it without asking the provider, and two polls racing past that
  check end with one file of one version.
- The audit row is the image call's: the model, the latency, the failure
  class; never the prompt.

### 2.3 The tool

`generate_video(prompt, seconds?, aspect?, fileName?, continue?)` in
media-tools:

- Submits, then waits for the video, polling, up to about 50 seconds a
  call. Done: the room file (artifact id and name). Not yet: `rendering`
  with the video's id, and the agent calls again with `continue` to keep
  waiting. An agent CLI may end a tool call at 60 seconds; a video never
  depends on one long call, and no clock ends the video itself.
- `seconds` (the model's lengths, 4, 6 or 8 for Veo) and `aspect`
  (`16:9` or `9:16`) are passed as the provider's `seconds` and `size`.

### 2.4 The relay waits as long as the call

- The bridge forwarded relay calls with an HTTP client that ends any call
  at 20 seconds, so a picture that took longer failed too. Relay calls are
  bound to the tool call that made them (its context), with no fixed
  limit (`no fixed time limits`); a stalled gateway still ends with the
  agent's tool call.
- `wire.RoomRelayRoutes` lists the two video routes; a route's `{id}`
  segment matches one path segment (never empty or a dot segment).

### 2.5 Later slices of this end state

- **A picture to film:** `inputs` (a room image as the first frame),
  the provider's `input_reference`.
- **Voices:** speech, and a cloned voice, when the model gateway serves
  one (none does yet, checked 2026-10-19); a capability `speech` the same
  way.

## 3. Design review

| Lens | Answer |
|---|---|
| Time and lifecycle | A video outlives a tool call; its id is the only state, held by the agent; a repeated poll is safe (one file). |
| Boundaries | Published to the job's room, as the job's requester; the provider's host is pinned (`orgHosts`). |
| Untrusted input | The prompt goes to the provider only; the MP4 is a room file, opened in the room's player. |
| One owner | The provider config, refusals, budget, audit and publish are the image path's, shared. |
| Component or platform | The provider key, budget and publish need the platform; the tool is the component's. |
| Load on agents | One tool beside `generate_image`; no new rule. |
| AI | The capability is AI; the agent writes the prompt from the member's ask. |
| Release safety | New routes and a new capability; nothing existing changes shape. The relay timeout change lets slow calls finish that failed before. |

Pre-mortem:
1. **The agent waits forever on a failed video.** The provider's
   `failed` status (and its error) is answered at once; `continue` on an
   unknown id is the provider's 404, answered as such.
2. **The same video lands twice.** The artifact id is derived from the
   video id, so a second publish is a version of the same file at most.
3. **A huge video.** The room's artifact cap refuses it, said plainly.

## 4. Build order (slice 1, shipped together)

1. Platform: capability, adapter calls, routes, report, console card,
   relay routes and timeout.
2. media-tools: `generate_video`.
3. Register the provider on QA (console, super admin), publish
   media-tools, deploy the gateway.
