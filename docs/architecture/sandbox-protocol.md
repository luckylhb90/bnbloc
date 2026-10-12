# Sandbox Protocol v1

The HTTP+JSON contract bnbloc uses to run agent commands in an isolated
Linux guest. It is to sandboxes what Core Protocol v1 is to agent cores: the
desktop and the bridge speak only this contract, never a specific VMM. Wire
types and a Go client live in [`sandbox/protocol`](../../sandbox/protocol);
the conformance suite in [`sandbox/conformance`](../../sandbox/conformance).
Design record: [`docs/prd/2026-10-09-agent-vm-sandbox.md`](../prd/2026-10-09-agent-vm-sandbox.md).

## Model

- A **machine** is one isolated Linux guest — a microVM on the member's
  computer (a Components-installed sidecar), or a remote VM behind an
  endpoint. bnbloc runs **one long-lived machine per member computer**.
- A **space** is a room inside the machine: a dedicated non-root guest user,
  its workspace mounts, a default exec environment and an env allowlist.
  Space key = `(room, core, provider)`, the bridge's session key (carried in
  `labels`).
- **Hybrid execution**: agent CLIs stay on the host; the shell commands they
  run are routed into the room's space through the exec endpoints. Host paths
  translate to guest paths through the space's mounts, so a wrapper can pass
  the host cwd unchanged (`hostWorkdir`).
- Mounts come in two kinds: `shared` — a sub-directory of a machine **share**
  (a host directory shared into the guest at machine creation; the guest sees
  the host's files live, both directions) — and `disk` — a guest-local
  directory (fast ext4; content moves through the files/archive endpoints;
  "clone mode").

## Conventions

- Base path `/sandbox/v1`. JSON bodies use the explicit camelCase names in
  `sandbox/protocol/types.go` (that file is the contract).
- Every `{machine}` path segment accepts the machine **id or its name**.
- Byte payloads inside JSON (`stdinBase64`, `dataBase64`, `stdoutBase64`,
  `stderrBase64`) are standard base64. File and archive bodies are raw
  (`application/octet-stream`, `application/x-tar`).
- Errors: every non-2xx answer is `{"error":{"code":"…","message":"…","retryable":bool}}`.
  Clients branch on `code`, never on the message or status.

| code | HTTP | meaning |
|---|---|---|
| `bad_request` | 400 | invalid input (incl. an image ref that is not an OCI reference) |
| `unauthorized` | 401 | missing or wrong bearer |
| `forbidden` | 403 | refused (browser `Origin`, guest permission denied) |
| `not_found` | 404 | no such route |
| `machine_not_found`, `space_not_found`, `exec_not_found`, `image_not_found`, `snapshot_not_found`, `pool_not_found`, `file_not_found` | 404 | the named resource does not exist |
| `path_not_mapped` | 422 | a host path lies outside every space mount / machine share |
| `invalid_state` | 409 | e.g. exec on a paused or stopped machine |
| `conflict` | 409 | name taken, snapshot still used by a pool, user clash |
| `capability_missing` | 501 | an optional capability the implementation does not offer |
| `version_unsupported` | 400 | protocol major mismatch |
| `limit_exceeded` | 429 | over a discovery limit |
| `image_pull_failed` | 502 | registry/pull failure |
| `timeout` | 504 | the operation timed out |
| `backend_unavailable` | 503 | the VMM/runtime is missing or broken |
| `internal` | 500 | anything else |

## Authentication

Every route except `GET /sandbox/v1/healthz` requires
`Authorization: Bearer <token>`. A local sidecar receives its token at launch
in the env var named by the package's `sidecar.tokenEnv` (the desktop
generates a fresh random token per launch) and must compare it in constant
time. A loopback sidecar must also refuse any request carrying an `Origin`
header (no browser page may drive it — DNS-rebinding defense) and must listen
on loopback only. Remote endpoints use the same bearer; its provisioning is
platform configuration, never part of the signed package.

## Versioning and discovery

`GET /sandbox/v1/discover`:

```json
{"name":"…","version":"0.1.0","contract":"bnbloc.sandbox.contract/v1",
 "protocol":{"min":"1.0","max":"1.0"},
 "capabilities":["exec","tty","spaces","shares","ports","disks","snapshots","pause","egressPolicy","secrets","fork","warmPool"],
 "backend":{"name":"…","version":"…","isolation":"microvm"},
 "limits":{"maxMachines":16,"maxSpaces":256,"maxCpus":12,"maxMemoryMiB":8192,"maxDiskMiB":65536,
           "maxFileBytes":67108864,"maxRunOutputBytes":16777216,"maxPoolSize":8}}
```

Version negotiation is the Core Protocol rule (`protocol.CompatibleRange`):
compatible when the ranges share a major version; minors are additive.
`backend.isolation` is `microvm`, `vm` or `os` (the weak tier) and must be
truthful — policy (borrowed drives require a VM) keys on it.

Capabilities: `exec` is mandatory in v1. Everything else is optional and
gated exactly like Core Protocol capabilities — not advertising one is
conforming, advertising one and failing it is not:

| capability | covers |
|---|---|
| `exec` | run / streamed exec, piped stdin, signals |
| `tty` | `tty`, `rows`/`cols`, resize |
| `spaces` | spaces and space-level exec/files (required by bnbloc's shared-machine mode) |
| `shares` | machine shares and `shared` space mounts |
| `ports` | published guest ports |
| `disks` | private block volumes |
| `snapshots` | full snapshots and restore |
| `pause` | pause / resume |
| `egressPolicy` | egress modes `allowlist` and `public` (`none` is always available) |
| `spaceEgress` | a per-space egress policy narrower than the machine's |
| `secrets` | host-substituted secrets |
| `fork` | live fork |
| `warmPool` | warm pools + claim |
| `execTokens` | space-scoped exec tokens (required by bnbloc's hybrid execution) |

## Endpoints

| method + path | body → answer |
|---|---|
| `GET /discover` | → Discovery |
| `GET /healthz` (no auth) | → `{"ok":true}` |
| `POST /images` | `{ref}` → Image (synchronous pull/prepare; cached refs return at once) |
| `GET /images` | → `{images}` |
| `POST /machines` | CreateMachineRequest → Machine (running) |
| `GET /machines?room=&core=&provider=&owner=` | → `{machines}` (label filter) |
| `GET /machines/{machine}` | → Machine |
| `DELETE /machines/{machine}` | → 204 (stops and removes; captured snapshots survive) |
| `POST /machines/{machine}/{start\|stop\|pause\|resume}` | → Machine |
| `POST /machines/{machine}/run` · `/exec` | ExecRequest → RunResult · NDJSON stream (root unless `user`) |
| `POST /machines/{machine}/execs/{execId}/stdin?eof=1` | raw bytes → 204 |
| `POST /machines/{machine}/execs/{execId}/resize` | `{rows,cols}` → 204 |
| `POST /machines/{machine}/execs/{execId}/signal` | `{signal}` → 204 |
| `PUT\|GET /machines/{machine}/files?path=&mode=` | raw bytes (root) |
| `PUT\|GET /machines/{machine}/archive?path=` | tar stream (root) |
| `GET /machines/{machine}/spaces?room=…` | → `{spaces}` |
| `PUT /machines/{machine}/spaces/{space}` | SpaceRequest → Space (idempotent ensure) |
| `GET\|DELETE /machines/{machine}/spaces/{space}[?purge=1]` | → Space · 204 |
| `POST /machines/{machine}/spaces/{space}/run` · `/exec` | ExecRequest (as the space user) |
| `POST /machines/{machine}/spaces/{space}/exec-tokens` | `{ttlSeconds}` → `{token, expiresAtMs}` (admin bearer only) |
| `PUT\|GET /machines/{machine}/spaces/{space}/files?path=\|hostPath=&mode=` | raw bytes (as the space user) |
| `PUT\|GET /machines/{machine}/spaces/{space}/archive?path=` | tar stream (as the space user) |
| `POST /machines/{machine}/snapshots` | `{name}` → Snapshot |
| `GET /snapshots` · `GET\|DELETE /snapshots/{id}` | → `{snapshots}` · Snapshot · 204 |
| `POST /snapshots/{id}/restore` | RestoreRequest → Machine (new) |
| `POST /machines/{machine}/fork` | ForkRequest → Machine (new) |
| `POST /pools` · `GET /pools` · `GET\|DELETE /pools/{name}` | CreatePoolRequest → Pool · `{pools}` · Pool · 204 |
| `POST /pools/{name}/claim` | ClaimRequest → Machine |

All paths are under `/sandbox/v1`.

## Machines

`CreateMachineRequest`: `image` (an OCI reference — implementations must
refuse host paths), `cpus`, `memoryMiB`, `disks [{name,sizeMiB,mountPath}]`,
`shares [{name,hostPath,guestPath?,readOnly?}]` (guest path defaults to
`/mnt/shares/<name>`), `env`, `workdir`, `ports [{guest,protocol?}]`,
`egress {mode,rules}`, `secrets [{name,value,hosts}]`, `labels
{room,core,provider,owner}`, optional unique `name`. The machine is running
when the answer is sent; `timings.createMs` reports the server-side cost.

- **Ports**: the implementation chooses the host side (loopback, free port)
  and reports `ports [{guest,protocol,hostIp,hostPort}]`.
- **Shares**: mounted root-only in the guest; guest-side ownership and mode
  changes must not alter the host files (virtualized ownership). Spaces reach
  a share only through their own mounts.
- **Egress** is deny-by-default on every implementation (an omitted policy is
  `none`). `allowlist` rules are `{host, port}` with `host` an exact DNS name,
  a `*.suffix` of at least two labels, or an IP literal (`port` 0 = any).
  `public` allows the public internet but never private, loopback or
  link-local ranges (including the host). Enforcement must sit outside the
  guest.
- **Secrets**: the guest sees env var `name` set to an opaque placeholder
  (reported as `placeholder`); the value is substituted on the host side only
  in egress to `hosts`, never returned by any endpoint, never logged.
  Substitution does not open egress — the hosts must also be allowed.
- **Lifecycle**: `stop` then `start` is a cold boot; the implementation
  re-establishes every space (users persist, mounts are re-bound). `pause`
  keeps memory resident; exec on a paused or stopped machine is
  `invalid_state`.

## Spaces

`PUT /machines/{machine}/spaces/{space}` with `SpaceRequest`:
`user` (default `sp-<space>`), `workdir`, `mounts
[{kind:"shared"|"disk", hostPath, guestPath, readOnly?}]`, `env`,
`envAllowlist`, `egress` (requires `spaceEgress`), `labels`. Ensure is
idempotent; a changed request converges (removed shared mounts are unmounted;
the user never changes). The answer reports the resolved `uid`/`gid`,
`home`, `workdir` and, per shared mount, the `share` it resolved into.

Requirements every implementation must meet:
- each space runs as its own non-root user; a space can neither read another
  space's mounts nor list a raw share (`spaceIsolation` in the suite);
- a `shared` mount's `hostPath` must lie inside a machine share
  (`path_not_mapped` otherwise); writes are visible to the host immediately
  and vice versa;
- `disk` mounts are guest-local directories owned by the space user;
- deleting a space kills its processes, unmounts its shares and removes its
  user; `purge=1` also removes its home and disk directories. Host share
  content is never deleted. Ensuring a space deleted without purge brings
  back the same user (uid) over its kept home and disks (`spaceRestart` in
  the suite): the bridge deletes a space after every sandboxed turn so no
  guest process outlives its turn, and re-ensures it for the next.

**Host path translation** (`hostWorkdir`, `?hostPath=`): the longest mount
whose `hostPath` contains the cleaned host path wins; `..` cannot escape a
mount. `protocol.MapHostPath` is the one rule; implementations and wrappers
must agree on it. Host paths are absolute POSIX paths in canonical form (the
space reports the resolved form).

## Space-scoped exec tokens

Capability `execTokens`. Hybrid execution hands an agent process a bearer;
it must never be the admin token. `POST
/machines/{machine}/spaces/{space}/exec-tokens` with `{"ttlSeconds":N}` (0 =
the implementation default; an implementation may shorten it — the answer's
`expiresAtMs` is authoritative) and the admin bearer answers
`{"token":"…","expiresAtMs":…}`. The token is a secret (never logged).

An exec token authorizes ONLY, as that space's user:
- `POST /machines/{machine}/spaces/{space}/run` and `…/exec` of THAT space
  (`{machine}` by id or name, as everywhere), and
- the control endpoints `POST /machines/{machine}/execs/{execId}/stdin|resize|signal`
  of execs started with the same token (piped stdin, Ctrl-C forwarding).

Every other route — discover, images, machines, lifecycle, machine-level
exec, other spaces, space GET/PUT/DELETE, files, archives, snapshots, pools,
and minting itself — answers 403 `forbidden` for an exec token. An expired or
unknown token is 401 `unauthorized`. Deleting the space (or its machine)
revokes its tokens; re-ensuring the space keeps them valid. The bridge mints
one per turn (`bnbloc-vmexec` uses it); the conformance step `execTokens`
checks all of the above.

## Exec

`ExecRequest`: `command` (argv; use `["sh","-c",script]` for shell syntax),
`env`, `workdir` (guest) or `hostWorkdir` (space only), `user` (machine level
only; at space level it must be empty or the space user), `stdinBase64` (whole
stdin, then closed) or `stdinPipe` (streamed exec only), `tty` + `rows`/`cols`,
`timeoutMs` (streamed exec: 0 = none; run: 0 = the implementation default).

At space level the process runs as the space user with `HOME`/`USER` set, the
space `env` overlaid with the request `env` filtered by `envAllowlist` (exact
names or `PREFIX*`; other names are dropped, never passed into the guest), and
cwd = `workdir` | translated `hostWorkdir` | the space workdir.

- **run** answers `{exitCode, stdoutBase64, stderrBase64, durationMs,
  timedOut, truncated}` once the process exits; output beyond
  `limits.maxRunOutputBytes` is dropped and flagged `truncated`. A timeout
  kills the process (`timedOut: true`, `exitCode: -1`).
- **exec** answers `application/x-ndjson`, one `ExecEvent` per line:
  `{"type":"started","execId":"…","pid":…}`, then any number of
  `{"type":"stdout"|"stderr","dataBase64":"…"}`, then exactly one terminal
  `{"type":"exit","exitCode":…,"timedOut"?,"durationMs"}` or
  `{"type":"error","error":{…}}`, after which the response ends. Errors before
  the process starts are ordinary error envelopes. Closing the response kills
  the process. NDJSON (not SSE) because the consumers are programs, not
  browsers: no event ids or reconnect semantics are needed, and a chunked
  response ending with the process is the natural lifetime.
- **Control** endpoints act on a running streamed exec by its `execId`:
  `stdin` (raw bytes; `?eof=1` closes stdin), `resize` (tty), `signal` (a
  POSIX signal number). After the terminal event the id is
  `exec_not_found`.
- Background processes must not keep the exec's stdout/stderr open, or the
  exec does not end (redirect them: `(cmd </dev/null >/dev/null 2>&1 &)`).

Exec is the hot path of hybrid execution; implementations should keep a
persistent host↔guest agent channel rather than spawning a process per
command (the suite's `execLatency` step reports p50/p95 and can enforce a
budget via `SANDBOX_EXEC_P95_MS`).

## Files and archives

`PUT …/files?path=<abs>[&mode=<octal>]` writes the body (parents created);
`GET …/files?path=` reads it (`file_not_found` when missing; files above
`limits.maxFileBytes` use the archive endpoints). `PUT …/archive?path=<dir>`
extracts a tar stream into the directory (created if missing); `GET
…/archive?path=<dir>` streams the directory as tar. At space level the
operations run as the space user (ownership follows; a permission failure is
`forbidden`) and files also accept `?hostPath=` (translated through the space
mounts).

## Snapshots, fork, warm pools

- `POST /machines/{machine}/snapshots` captures disk + memory + device state
  (`kind: "full"`); the source keeps its state. The snapshot records the
  machine's spaces and shares. An implementation that cannot restore some
  captured resource (e.g. secret bindings) must refuse the snapshot with
  `capability_missing` rather than produce an unrestorable one.
- `POST /snapshots/{id}/restore` creates a NEW running machine without a boot.
  Image, CPU, memory, disks, spaces and secret bindings come from the
  snapshot; ports and labels are bound fresh; each captured share is re-bound
  to the host path given by name in `shares`, else to its captured path;
  `egress` (when set) replaces the captured policy; `cowMemory` (default true)
  maps snapshot memory copy-on-write.
- `POST /machines/{machine}/fork` continues the source's live execution in a
  new machine (copy-on-write) without a durable snapshot; writes in the child
  never reach the source.
- A **warm pool** keeps `size` machines restored from a snapshot; `claim`
  hands one over immediately (`source.warm: true`, labels and name applied)
  and the pool refills in the background. An empty pool restores on demand
  (`warm: false`) instead of failing. A snapshot used by a pool cannot be
  deleted (`conflict`); a claimed machine outlives its pool.

`timings` report `createMs`, `restoreMs`, `claimMs` and the last lifecycle
operation.

## Component packaging

A sandbox implementation ships as a Components package of kind
`bnbloc.sandbox.package/v1` with contract `bnbloc.sandbox.contract/v1`,
validated in `core/pkgmeta` (protocol range compatible, capabilities from the
v1 set and including `exec`):

- `runtime: "sidecar"` (local): per-platform `artifacts`, loopback
  `sidecar.localUrl`, `listenEnv`, `supervisedEnv`, a **required**
  `tokenEnv`, optional `dataDirEnv`, and `sidecar.files` — extra artifacts the
  sidecar needs beside its executable (a VMM helper, a firmware library)
  published as `artifacts["<platform>/<name>"]`, downloaded and
  sha256-verified like the executable, their local paths handed over in the
  declared env vars; a file named `bnbloc-vmexec` is the command router the
  desktop hands the bridge (`BNBLOC_SANDBOX_VMEXEC`);
- optional `guest: {image, cpus?, memoryMiB?}` (sandbox kind only): the
  machine profile the desktop prewarms — `image` is an OCI reference chosen
  by the publisher (never a host path, never baked into the desktop);
- `runtime: "endpoint"` (remote): `endpoint.baseUrl` whose host is in
  `permissions.network`.

```json
{
  "kind": "bnbloc.sandbox.package/v1",
  "contract": "bnbloc.sandbox.contract/v1",
  "name": "vm-runtime", "version": "0.1.0", "publisher": "acme",
  "protocol": {"min": "1.0", "max": "1.0"},
  "runtime": "sidecar",
  "capabilities": ["exec", "tty", "spaces", "shares", "ports", "disks", "snapshots", "pause", "egressPolicy", "secrets", "fork", "warmPool"],
  "sidecar": {
    "localUrl": "http://127.0.0.1:7311",
    "listenEnv": "VMRT_LISTEN", "supervisedEnv": "VMRT_SUPERVISED",
    "tokenEnv": "VMRT_TOKEN", "dataDirEnv": "VMRT_DATA_DIR",
    "files": [
      {"name": "vmm", "env": "VMRT_VMM_PATH", "executable": true},
      {"name": "firmware", "env": "VMRT_FIRMWARE_PATH"},
      {"name": "bnbloc-vmexec", "env": "VMRT_VMEXEC_PATH", "executable": true}
    ]
  },
  "guest": {"image": "registry.example/agent-toolbox:1", "cpus": 2, "memoryMiB": 2048},
  "artifacts": {
    "darwin-arm64": {"url": "", "sha256": ""},
    "darwin-arm64/vmm": {"url": "", "sha256": ""},
    "darwin-arm64/firmware": {"url": "", "sha256": ""},
    "darwin-arm64/bnbloc-vmexec": {"url": "", "sha256": ""}
  }
}
```

Installed through `POST /v1/platform/components` (or `bnbloc cores install
--package`), it follows rollout and tenant scope like every component and is
listed in the member `GET /v1/components` (with `sidecar`, `artifacts`,
`capabilities` and `guest`) for the desktop. The gateway never dials a sandbox
(it holds no bearer).

## Desktop supervision

The desktop sidecar manager runs at most one sandbox component per computer
(several installed: the lexically first name runs, the others report
"inactive"). It lays the executable and every declared file out in one
per-version bundle directory with the declared names (each sha256-verified,
executables `+x`), launches the sidecar on a fresh loopback port with a fresh
bearer in `tokenEnv`, a private owner-only data dir in `dataDirEnv`,
`supervisedEnv=1` and each file's path in its env, polls `GET /discover`
until it answers (contract, protocol and the `exec`/`spaces`/`shares`
capabilities are required), and then prewarms the one machine `bnbloc`:
`POST /images` for the guest image, then create (or start) it with the share
`workspaces` = `<home>/bnbloc-workspaces` at the identical guest path and
egress `public` when `egressPolicy` is offered. Only then does it hand the
bridge `BNBLOC_SANDBOX_*` (see the PRD's "Phase 1 contracts"). Mode `off`
deletes the machine.

## Conformance

```
SANDBOX_URL=http://127.0.0.1:7311 SANDBOX_TOKEN=… go test ./sandbox/conformance -run TestConformance -v
```

Optional: `SANDBOX_IMAGE` (default `alpine:3.20`), `SANDBOX_SHARE_DIR` (a
temp dir is used for a loopback endpoint), `SANDBOX_NETWORK_TESTS=1` (egress
allowlist against the internet), `SANDBOX_EXEC_P95_MS` (latency budget). The
suite checks discovery, auth, the error envelope, images, machine create and
exec, spaces (users, isolation, host-path translation, live shared mounts,
env allowlist, file ownership), exec latency, piped stdin, tty, signals,
secrets (never plaintext in the guest or any answer), exec-token scoping,
egress deny-by-default,
ports, files/archives, disks, pause/resume, stop/start, snapshot/restore,
fork, warm pools and deletion, logging the timings of each step.
