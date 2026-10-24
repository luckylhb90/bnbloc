# Connections: in place, and configured from words (2026-10-25)

Extends docs/prd/2026-10-22-connections.md and
docs/prd/2026-10-23-connections-ai.md. Personal agents that connect to
apps (a connector per app, read and write told apart, a person's
approval for a write) set the bar: nothing should send a person
somewhere else to say yes.

## 1. In place

- **Cards act where they are.** A card action of kind `call`
  (`component.CardCall`) is posted by the client to
  `/v1/my/component-cards/{service}/{page}`. It reaches the service's
  `/cards/{page}` as the member, for the card's room, and the card shows
  the service's `CardReply`: its text, an https address to open, and
  whether it is done.
  - A held change's card has **Allow** and **Don't allow**: the change is
    made or refused there, and the card says what the system answered.
    **Review** still opens the page, with "allow without asking from now
    on".
  - **Connect** opens the system's sign-in at once (OAuth) or asks the
    provider (SSO). Only a key to paste opens the Connections page.
- **One owner per decision.** Deciding a held change (`decide`) and
  connecting (`connectTo`) serve the page and the card alike.

## 2. Limits removed

- **Signing by the secret's kind**, nothing to configure: a shared secret
  signs HMAC-SHA256 (hex); a PEM private key signs Ed25519 or RSA SHA-256
  (base64). The key is read even when pasted on one line.
- **Signed requests from the member's computer**: `EgressSend.RawQuery`
  and `ReplaceBody` carry the exact query string and body the component
  signed, so a signing connector may send from members' computers too.

## 3. Configuring, AI first

The admin's first path is words. The JSON stays beside it as the manual
fallback.

- **Connect a system**: the admin describes it, and may paste its API
  documentation. The AI drafts the connector
  (`connections-draft-connector`). It arrives as a draft with its review,
  and the admin adds it.
- **A review finding carries its fix**: a JSON merge patch (RFC 7386) that
  makes a valid connector. The admin applies it with one click and the
  connector is reviewed again. A fix never enables, renames, or moves
  where requests go out from.
- **Choices are inferred, not asked.** The signature follows the secret's
  kind, and a key and its secret are two fields. Discovery fills OAuth
  endpoints. Room identity reads the platform's attested room.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | Members decide in the card; admins describe and apply fixes; the AI drafts and proposes. |
| Abuse and untrusted input | A card's call carries only the action's query and the room; the service checks the member (and the room role) as on its pages. An AI draft is a draft, and a fix is validated, cannot enable, and needs a click. |
| Load on users and agents | Agents: nothing new. Members: one click in the card. Admins: a sentence instead of JSON. |
| Release and compatibility | The card kind needs the desktop release that knows it (an older bridge refuses the card), and the web that renders it. |
| AI capability | §3. |
| Component or platform | The platform adds the card call route and the send's exact query and body; everything else is the component's. |
