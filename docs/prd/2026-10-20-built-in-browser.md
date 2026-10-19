# A built-in browser (2026-10-20)

Status: decided (direction A now; the desktop shell, B or C, decided later
by §4). Extends `docs/prd/2026-10-17-browser-component.md` §2.2 (the
browser host).

The desktop ships and runs its own Chromium for agents' browser runs, the
member sees and takes over a run's page inside the app, and agents keep the
full Chrome DevTools Protocol (CDP) on every platform.

## 1. Why

Runs drive the member's installed Chrome in a dedicated profile, started by
the browser component itself. Every hard failure of QA week 2026-10-19 came
from that:

- The component runs inside the agent's sandbox, which cannot start a
  process outside it: a browser it closed to change flags could not be
  started again.
- An installed Chrome is whatever version and flags the machine has: a
  browser started by an older component lacked the flags that keep
  background windows painting.
- The run's window lives behind the member's others, and the member's view
  of it is a picture relayed through the gateway, a few frames a second.
- No Chrome installed, no runs.

The browser PRD's end state already says the desktop launches and
supervises the browser; it was never built.

## 2. What it is (direction A)

### 2.1 The desktop owns the browser

- The desktop starts, supervises and restarts one browser for the member
  (crash: restart; the next run reopens its tab). It is a desktop
  component (a sidecar), never a process the browser tool starts.
- CDP runs over a pipe (`--remote-debugging-pipe`), held by the desktop
  only. The browser tool reaches it through the desktop's local endpoint,
  authenticated with a per-session token, so no other local process can
  drive the member's signed-in browser (today a loopback debugging port
  is open to any local process).
- The profile stays the dedicated one under the desktop's data directory;
  the member's everyday browser is never touched.

### 2.2 Its own Chromium, kept current

- The browser runtime is a component: Chrome for Testing (Google's
  Chromium build for automation) of one pinned version, its binaries
  stored as the component's artifacts (sha256-pinned, signed package),
  downloaded on first use, never in the installer.
- It follows Chrome stable: a scheduled job in the browser's repo bumps
  the pinned version when a stable release lands, runs the real-Chrome
  tests, and publishes it (optin, then auto). A runtime more than two
  weeks behind stable says so in the desktop.
- Without the runtime (not yet downloaded, an unsupported platform), the
  desktop supervises the installed Chrome as today's fallback.

### 2.3 The page inside the app

- A run's page shows in the side panel, streamed locally: the desktop
  reads frames over CDP and hands them to the app's view directly (not
  through the gateway), so it is smooth on the same machine.
- The member drives it there: pointer, wheel, keys, input methods (CJK
  composition), copy and paste, the cursor's shape; the same input path
  as today's live view, local.
- "Open in a window" brings the browser's real window forward for what a
  picture does poorly (a hard sign-in, a drag); the run goes on in it.
- Members watching from another device keep today's live view through
  the gateway, at its lower rate.
- To settle in the first slice (a spike): the browser mode that gives both
  a steady frame rate and the pop-out window without losing the page
  (a headful window kept off-screen with background throttling off, or a
  headless browser relaunched headful).

### 2.4 The agent's side does not change

The browser tool speaks CDP to "a browser" (browser PRD §2.2); it now asks
the desktop for it. The tools, checkpoints, replay and consent rules stay.

## 3. Later: the page natively in the app (the desktop shell)

Streaming is the step; the end state is the browser rendered natively in
the app's window, which needs a Chromium-based desktop shell:

| | B: Tauri with its CEF runtime | C: Electron |
|---|---|---|
| Rendering | Chromium | Chromium |
| The app's own overhead | Rust main process: lighter | a Node.js runtime besides: tens of MB more |
| Maturity | CEF is mature; Tauri's CEF runtime is a 3.0 alpha, experimental | a decade of large apps |
| Browser views in the app | CDP from Rust; several views, sessions and crash recovery still maturing | first-class: `WebContentsView`, session partitions, `webContents.debugger`, offscreen rendering, `render-process-gone` |
| Security updates | follows Chromium; Tauri's cadence unproven | a major every eight weeks with Chromium, three majors patched |
| The app's own security model | Rust backend, capability-scoped IPC | safe defaults; history of misconfigurations |

Today C is the safer end state for an app whose agents drive browsers in
it; B could be lighter and keeps the Rust code, if Tauri's CEF runtime
matures. The desktop's display of a run is one seam (frames now, a native
view then), so either replaces the stream without touching the browser
tool.

## 4. How the shell is decided

When Tauri's CEF runtime is stable, build the same spike in both and
measure on macOS and Windows:

1. Five browser views driven at once over CDP, plus the member typing in
   one (CJK input).
2. An enterprise SSO sign-in in a view, kept across restarts.
3. Eight hours of runs: memory, renderer crashes and recovery.
4. Cold start time and idle memory of the app.
5. A Chromium security release reaching members: days from upstream.

The one that wins 1–3 without losing badly on 4–5 is the shell.

## 5. Design review

| Lens | Answer |
|---|---|
| Time and lifecycle | The desktop restarts the browser and reopens runs; a new runtime version lands at the next restart, never under a running run. |
| Boundaries | CDP is the desktop's alone (a pipe), lent to the browser tool with a token; the profile is the dedicated one. |
| Release safety | The runtime follows stable through optin first; the installed Chrome stays the fallback; the tool's interface does not change. |
| Component or platform | The runtime and the browser tool are components; the desktop supervises, as it does other sidecars. |
| Load on users | Nothing to install by hand; one panel to watch and take over. |
| Untrusted input | Pages are untrusted as today; the stream carries pictures in and input out, nothing else. |
| AI | Unchanged: the agent drives, the decision model finds and guards (browser PRD). |

## 6. Pre-mortem

1. **A stale Chromium browses the web.** The weekly bump job, the
   two-week warning, and optin-then-auto rollout keep it current.
2. **Another local process drives the signed-in browser.** No debugging
   port: a pipe the desktop holds, a token per session for the tool.
3. **The stream is too slow to sign in through.** "Open in a window" is
   always there; the spike settles the frame rate first.
4. **The download is too big for some members.** It is on first use, in
   the background; the installed Chrome works meanwhile.
5. **The shell choice never gets made.** §4 names when and how.

## 7. Build order

1. The desktop supervises the browser (the installed Chrome), CDP over a
   pipe, the tool's token-authenticated endpoint.
2. The browser runtime component (Chrome for Testing, pinned, the
   weekly bump job), with the installed Chrome as fallback.
3. The page in the side panel: the local stream, full input, open in a
   window; the spike decides the mode.
4. The shell decision (§4), when Tauri's CEF runtime is stable.
