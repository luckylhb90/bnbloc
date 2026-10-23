# Connections review findings (2026-10-23)

A review of the egress proxy, the turn proxy, the component pages and the
connections component (bnbloc-connections) found the following. Fixed in
bnbloc-connections v0.2.1 and, for the bridge, in this repository.

| # | Finding | Fix |
|---|---|---|
| 1 | `GET /api/%2e%2e/admin/users` matched the read rule `/api/**` (the policy read the decoded path, `**` ate `..`) and the system served `/admin/users`. | `connector.CheckPath` refuses a `.`/`..` segment (also encoded once or twice, or before `;`), an encoded slash and a backslash before the policy (`400`). |
| 2 | Concurrent requests with no token in memory refreshed one grant's refresh token twice; a provider with reuse detection revokes the grant. | One grant's credentials are made one at a time, from the grant as stored then. The component runs as one replica. |
| 3 | A second held change in one turn got no card: cards were kept once per job, service and title, and every held change of a system has the same title. | Bridge: once per job, service, title and what the card's actions open (`cardKey`). Component: the Connections page lists every waiting change. |
| 4 | A sandboxed command reached a connector host that is also one of the member's private hosts directly (`NO_PROXY`): no credentials, no card, no record. | Private hosts are no longer in `NO_PROXY`; the proxy's sandboxed tunnels reach a private address only for them (`sandboxReaches`). |
| 5 | A CLI that trades its key for a token first (`POST …/exchange {token}`) could not be connected. | `apiKey.exchange` (the component trades the key) and `answers` (it answers the CLI's own exchange step). |
| 6 | Every agent process of an organization with an egress service runs with `HTTPS_PROXY`, so its model traffic is tunnelled through the bridge. | Not changed: a session restored after a bridge restart keeps the room tools server's address and bearer (`roomtools.json`, `roomtools.key`), so only the restart window and streams open across it are cut. Measuring a restart mid-turn is open. |
| 7 | Each request read and wrote the member's activity on its path; replicas would race it. | Activity is queued and written off the path; one replica. |
| 8 | An allowance from the member's computer held in any room; a disconnect left waiting changes and allowances; "allow without asking" on the review page did not ask the OAuth write scopes. | Allowances are bound to their room; a disconnect drops them; allowing a change on a sign-in without the write scopes signs in again for them (`Grant.Scopes`). |
