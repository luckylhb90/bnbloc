# Four-character room invite codes (2026-10-22)

Status: built (2026-10-22), not yet deployed. Owner: the room service's
invite policy (`services/room`), the gateway's redemption throttle
(`services/gateway`), the invite constants (`internal/wire/invites.go`) and
the web's invite dialogs (`web/src/lib/inviteCode.ts`).

A private room is joined with an invite code. It was 32 hex characters,
unreadable aloud and a chore to type. It is now four characters of a 31-symbol
alphabet (no `0 O 1 I L`), so it can be said across a table and typed on a
phone. The owner asked for it ("easier to type").

## 1. The cost, stated plainly

31^4 = 923,521 codes. A code is the only gate of a private room, so a short
one is guessable by anyone in the organization who is willing to try: redemption
is looked up per tenant (`RedeemInvite` filters by the caller's tenant), so one
guess tests every live code of the caller's organization at once. With N live
codes and G guesses a day an insider succeeds with probability about N*G/923521
a day. The old throttle (5 a minute, burst 10: 7,200 a day) made that 14% a day
at N=20. The policy below brings it to about 0.5% a day at N=20 (and 0.03% at
N=1). It is a bound, not a proof: a private room that must not be reachable by
an insider who tries needs a longer code or no code (an invite by member id).

## 2. Policy

- **Alphabet and length:** `wire.InviteCodeAlphabet`, `wire.InviteCodeLen`
  (one owner; the web is generated from them). Drawn uniformly (rejection
  sampling) from `crypto/rand`.
- **Short life:** default 1 day, at most 7 days (was 7 and 30). The web offers
  1 or 7 days.
- **Throttle:** redemption is limited per subject by its own budget
  (`inviteAllow`): burst 5, 10 an hour (240 a day). It no longer shares the
  guess budget of login and share links.
- **One namespace, kept free:** the code is the table's primary key (any
  tenant's), so a creator first deletes the invites nobody can redeem (expired,
  or used to capacity: `PurgeDeadInvites`, every tenant) and draws again, up to
  16 times, when a code is taken. A code used up or expired then answers
  "not found" instead of "full"/"expired" once purged.
- **Typing:** the web upper-cases, drops what a code never has (spaces,
  dashes, `0 O 1 I L`) and stops at four characters (`normalizeInviteCode`).
  The server matches a code exactly (it only ever issues upper case).

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | Owners and admins create codes; any signed-in member of the organization may try one. A guesser is an insider; an outsider has no session. |
| Time and lifecycle | A code lives at most 7 days and is deleted once dead, at the next creation of any invite. A used or expired code stops being distinguishable from a wrong one. |
| Scale and cost | The purge is one DELETE by two columns on a table that holds only live invites plus what died since the last creation. Live codes are bounded by creation rate times 7 days. |
| Failure and recovery | A failed purge fails the create (500, nothing stored). 16 collisions fail the create (500); at under a million codes and a purged table that means the namespace is nearly full, which the operator sees as errors. |
| Boundaries and privacy | Lookup stays per tenant, so a guess never reaches another organization's room. Creation can collide with another tenant's code and redraws, revealing nothing to the creator. |
| Abuse and untrusted input | The throttle is per subject at the edge (spent on every attempt). The input is a path segment, redacted from logs as before (`obs`). A scripted insider is the threat §1 bounds; per-subject limits do not stop an insider with several accounts. |
| One owner | Constants in `wire`; the policy in the room service; the budget in the gateway; typing rules in one web function. |
| Defaults and fallbacks | Default expiry 1 day. A request that names a longer expiry is capped, not refused. |
| External contracts | The create answer is unchanged (`code` is shorter). A client that parsed a 32-character code breaks; none in this repo does. |
| Load on users and agents | Typing four characters instead of 32; agents never see a code. |
| Release and compatibility | Pre-launch: no migration. Live 32-character codes keep working until they expire (`VARCHAR(64)`). |
| Observability | The 429 on redemption is the existing edge answer; a redraw is silent. A metric for redraws is not added: it would be the signal that the namespace is filling. |
| AI capability | None. |
| Component or platform | Platform: it is the gate of a room. |

**Pre-mortem.**
1. *An insider guesses a private room's code.* The throttle (240 a day), the
   short life and the per-tenant lookup bound it; §1 says what the bound is.
   A gateway test holds the budget; a room test holds the TTL cap.
2. *The namespace fills with dead codes.* Creation purges them first; a
   contract test holds the purge on both stores.
3. *Two creators draw the same code at once.* The primary key refuses the
   second insert (`ErrInviteExists`) and it draws again; a service test holds
   the retry and its bound.
4. *A member types `O` for `0`.* Neither is in the alphabet; the web drops them
   and the server never issues them.
