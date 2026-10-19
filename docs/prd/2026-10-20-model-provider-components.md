# Model providers are components (2026-10-20)

Status: proposed. Builds on `docs/prd/2026-10-19-component-settings.md`
(the settings contract). Owner: the component contract, the provider
resolution, the protocol adapters.

Every model the platform calls (the assist model, the decision model, image
generation, speech to text, video generation) comes from an installed
**provider component**. The platform installs a default; an organization
installs its own; a member may bring their own. Nothing about a provider is
hard-coded in this repo except the wire protocols it speaks.

## 1. Why

Today three settings families hold the models, each the same shape twice
(the platform's, an organization's override), with its own routes and its
own console card:

| Capability | Setting | Code |
|---|---|---|
| Assist (chat, JSON) | `assist.model` | `internal/assist/settings.go`, `PlatformAssistModel.tsx`, `OrgAssistModel.tsx` |
| Decision (calibrated choices) | `decision.decide` | `internal/decision/decision.go`, a card in `PlatformMediaProviders.tsx` |
| Image, transcribe, video | `media.<capability>` | `internal/media/media.go`, `PlatformMediaProviders.tsx`, `OrgMediaProviders.tsx` |

- A new vendor's parameter, or a new capability, is a change here (Go
  keys, wire types, routes, a form; the web even repeats the adapter
  kinds).
- An organization can only override what the platform offers, in the
  platform's console. A member cannot use their own model at all.
- The media-tools component's real configuration is these settings: the
  component is configured in another repo's console.

## 2. What it is (the end state)

### 2.1 A provider component

A new component kind, `bnbloc.provider.package/v1`:

```json
{
  "kind": "bnbloc.provider.package/v1",
  "name": "team-gateway",
  "version": "1.0.0",
  "displayName": "Team model gateway",
  "protocol": "openai-compatible",
  "serves": {
    "assist": {"models": ["chat-large", "chat-small"]},
    "image": {"models": ["image-1"]},
    "transcribe": {"models": ["asr-1"]},
    "video": {"models": ["video-1"]}
  },
  "settings": [
    {"key": "baseUrl", "label": "Base URL", "type": "url", "scope": "org", "required": true},
    {"key": "apiKey", "label": "API key", "type": "secret", "scope": "org", "required": true},
    {"key": "imageSize", "label": "Landscape size", "type": "text", "scope": "org", "default": "1536x1024"}
  ]
}
```

- `protocol` names a wire protocol family the platform implements:
  `openai-compatible` (chat, images, audio transcriptions, videos) and
  `openai-decisions` (calibrated choices) today. A protocol is platform
  code, like Core Protocol v1 for cores; a vendor is never named in it.
- `serves` lists the capabilities and their models (the first the
  default). A provider may serve one or several.
- `settings` is the settings contract: the base URL, key and the
  protocol's parameters, with their scope. A protocol declares which
  setting keys it reads (`baseUrl`, `apiKey`, its params), so a package
  that misses a required one is refused at install.
- A provider package is data only (no binary), so it is safe for an
  organization to install (§2.2).
- Later: an **endpoint provider** speaks a platform Provider Protocol v1
  itself (a service beside the gateway), for a vendor no protocol fits.

### 2.2 Who installs, and who chooses

| Level | Installs | Chooses per capability |
|---|---|---|
| Platform | the super admin, offered to every organization | the platform default |
| Organization | an organization admin, for their organization only (new: today only the super admin installs) | the organization's provider for each capability, from the platform's and its own |
| Member | a member adds their own provider for themselves (their own key), when their organization allows it | their own provider for each capability |

- A call resolves its provider per capability: the member's own (when
  allowed and set) → the organization's choice → the platform default.
  None: the capability is off for that caller (as an unconfigured one is
  today).
- The organization's policy: whether members may bring their own, and for
  which capabilities. Organization AI off turns every provider off for its
  members, their own included.
- An organization or member provider's outbound host is its own `baseUrl`'s
  host only (extgov's guard), never a private, loopback, link-local or
  metadata address; a platform provider may name an internal host.

### 2.3 Budget, audit, what leaves

- A call on the platform's or the organization's provider spends the
  organization's AI budget, as today. A call on a member's own provider
  spends no organization budget (they pay their vendor); it is still rate
  limited and audited.
- Every call's audit row names the provider component and its level, the
  model, latency and failure class; never the input.
- The settings form of a provider says what leaves: "the requests your
  members make through this provider are sent to <its host>".

### 2.4 The console

- Components → Providers: install a provider package (or, for the common
  case, fill a new one from a template the platform ships, e.g.
  "OpenAI-compatible gateway"), its settings (the one form), Test (the
  protocol's cheap check, e.g. listing models; never a billable call).
- Organization → AI: for each capability, which provider serves it.
- Settings → AI (a member): their own providers, when allowed.
- The three hand-written families of cards go.

### 2.5 AI

- Test explains: a failed test is explained by the assist against the
  provider's declaration ("this gateway answers 404 on /videos: it does
  not serve video").
- Fill from a note (the settings PRD's §2.5) fills a provider from a
  vendor's setup note.

## 3. Design review

| Lens | Answer |
|---|---|
| One owner | One resolution (member → organization → platform) for every capability; one protocol per wire family; one settings contract; the three settings families and their routes and cards are deleted. |
| Component or platform | The platform keeps the protocols, resolution, budget, audit and the network guard; which vendor, model and parameters is the component's. |
| Boundaries | An organization's provider serves only it; a member's only them; keys sealed per scope, never shown back. |
| Untrusted input | An organization- or member-installed package is data only, bounded and validated; its host cannot be internal. |
| Load on users | One place per level; templates for the common gateway. |
| Release safety | Pre-launch: the settings families are replaced, not kept beside; QA's three providers are installed again as provider components (no backfill). |
| AI | Test explained; fill from a note. |

## 4. Pre-mortem

1. **An organization points a provider at an internal service (SSRF).**
   Organization and member providers cannot target private, loopback,
   link-local or metadata addresses; only a platform provider can.
2. **A member's key leaks to others.** It is sealed under the member's own
   record and used only for their calls.
3. **A member runs up a bill on the organization.** Their own provider
   bills their own vendor; the organization's budget is untouched.
4. **The decision model is swapped for one that is not calibrated.** The
   decision capability needs the `openai-decisions` protocol; a chat
   provider cannot serve it.
5. **Losing the old configuration.** The QA providers are entered again
   once (three of them); the form says what is missing.

## 5. Build order (slices of §2)

1. **The provider kind, platform level.** `bnbloc.provider.package/v1`,
   protocols declaring their settings, resolution, the Providers console
   at platform level; assist, decision and media move onto it; the three
   settings families go. Needs the settings contract's slice 1.
2. **Organization providers.** Organization install of data-only provider
   packages, the per-capability choice, the network rule.
3. **Members' own providers.** The policy, the member's settings, budget
   exemption.
4. **Endpoint providers.** Provider Protocol v1.
