import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { PlayIcon, StopIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import {
  artifactRunnerUrl,
  composeArtifactDocument,
  contentKey,
  loadMessage,
  readGuestMessage,
  type ConvertFileSrc,
} from "@/lib/artifactFrame";

// ArtifactRunner is the room's ONE execution surface for live artifacts (kind
// "app"): self-contained HTML/JS an agent or member produced — an interactive
// diagram, a game, a dashboard prototype. It renders inside a sandboxed iframe
// and bridges a real-time collaboration channel so every member viewing the
// same artifact shares one evolving state.
//
// Security model (the reason this is the only execution surface):
//   - sandbox="allow-scripts" WITHOUT allow-same-origin: the guest cannot
//     touch the parent's origin — no cookies, no session token, no DOM access
//     to the app, no same-origin fetch of room APIs
//   - the frame loads the artifact runner BY URL from its own origin (desktop:
//     the shell's bnbloc-artifact scheme; browser: the web bundle), never via
//     srcdoc, so the app's CSP stays strict (no inline script in the app)
//   - CSP inside the artifact doc: no external script/style/image origins
//   - the ONLY channel out is postMessage, validated by origin ("null" for
//     sandboxed frames), source frame and message shape (lib/artifactFrame)
// Anything the artifact wants beyond its own box must go through the state
// channel, which the host routes to the room — never to the member's session.
//
// Collaboration protocol (both directions, room-scoped and transient):
//   guest  -> host : { type: "artifact-state", state: <json value> }   (actor intent)
//   host   -> guest: { type: "artifact-state", state, actor, broadcast } (sync)
// The host dedupes its own echoes and forwards state to the room stream via
// onState; incoming remote state (onRemote) is delivered as-is. The guest is
// expected to merge/appliance state idempotently; the protocol carries no
// ordering guarantees beyond the room stream's own sequence.
function runnerUrl(): string {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return artifactRunnerUrl({
    convertFileSrc: internals?.convertFileSrc,
    baseURI: document.baseURI,
    hostOrigin: window.location.origin,
  });
}

export function ArtifactRunner({
  roomId,
  actor,
  artifactId,
  version,
  src,
  height = 360,
  autoRun = false,
  reloadToken = 0,
  onState,
  remoteState,
}: {
  // Owning room; present only where the host can authorize the join snapshot.
  roomId?: string;
  // Viewer's identity, handed to the guest on readiness so live artifacts can
  // attribute actions (per-user colors, cursors). Room-scoped public data
  // (the roster shows it); the sandbox cannot exfiltrate it — opaque origin,
  // CSP default-src 'none', and the host validates every inbound message.
  actor?: string;
  artifactId: string;
  // The version this content belongs to; a version change re-inits the guest.
  version: number;
  // Self-contained HTML document (the artifact's current content).
  src: string;
  // A pixel height, or "fill" to take the parent's full height (the
  // workbench tab).
  height?: number | "fill";
  // Start running on mount (opening a workbench tab is the explicit intent);
  // otherwise the member clicks "Run live preview" first.
  autoRun?: boolean;
  // Bumping it reloads the guest (a fresh frame and document).
  reloadToken?: number;
  // Local actor's state change (from the guest) to forward to the room.
  onState?: (state: unknown) => void;
  // Latest remote state received from the room, delivered to the guest.
  remoteState?: { state: unknown; actor: string; nonce: number };
}) {
  const [running, setRunning] = useState(autoRun);
  // Guest readiness + join snapshot: the bootstrap script pings on load (all
  // inline scripts registered their listeners by then), and only then does
  // the host inject the last known board state — never before, so the
  // snapshot cannot be lost to a not-yet-registered listener.
  const [guestReady, setGuestReady] = useState(false);
  const [snapshot, setSnapshot] = useState<{ state: unknown; actor: string } | null>(null);
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const lastSentRef = useRef<string>("");
  const remoteNonceRef = useRef(0);
  // The document the runner writes, and whether this mounted frame already
  // got it: the runner accepts exactly one, and the guest could replay a
  // runner-ready ping later — it never gets a second hand-off.
  const doc = useMemo(() => composeArtifactDocument(src), [src]);
  const docRef = useRef(doc);
  docRef.current = doc;
  const handedOffRef = useRef(false);
  const actorRef = useRef(actor);
  actorRef.current = actor;

  // A version change remounts the guest: readiness resets (the new document
  // re-pings) and the snapshot refetches in case the board advanced.
  useEffect(() => {
    setGuestReady(false);
    if (!running || !roomId) return;
    let cancelled = false;
    setSnapshot(null);
    api
      .getArtifactState(roomId, artifactId)
      .then((snap) => {
        if (!cancelled && snap) setSnapshot({ state: snap.state, actor: snap.actor });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [running, roomId, artifactId, version, reloadToken]);

  // Inject the join snapshot once the guest is ready to receive it.
  useEffect(() => {
    if (!running || !guestReady || !snapshot || !frameRef.current) return;
    frameRef.current.contentWindow?.postMessage(
      { type: "artifact-state", state: snapshot.state, actor: snapshot.actor, broadcast: true },
      "*",
    );
    setSnapshot(null);
  }, [running, guestReady, snapshot]);

  // Deliver remote state into the guest. The nonce dedupes replays; the guest
  // decides how to merge (it owns its document state).
  useEffect(() => {
    if (!remoteState || !frameRef.current || !running) return;
    if (remoteState.nonce === remoteNonceRef.current) return;
    remoteNonceRef.current = remoteState.nonce;
    frameRef.current.contentWindow?.postMessage(
      { type: "artifact-state", state: remoteState.state, actor: remoteState.actor, broadcast: true },
      "*",
    );
  }, [remoteState, running]);

  // Receive the runner's hand-off request and the guest's readiness/state
  // intents. Registered at mount (before any frame exists), so the runner's
  // ready ping can never race the listener.
  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const frame = frameRef.current;
      const msg = readGuestMessage(ev, frame?.contentWindow);
      if (!msg || !frame) return;
      switch (msg.kind) {
        case "runner-ready":
          if (handedOffRef.current) return;
          handedOffRef.current = true;
          // The target origin of a sandboxed frame is opaque: "*" is the only
          // form postMessage accepts; the source check above pins the frame.
          frame.contentWindow?.postMessage(loadMessage(docRef.current), "*");
          return;
        case "ready":
          setGuestReady(true);
          // Identity handshake: tell the guest who is viewing, so it can
          // attribute this viewer's gestures without guessing.
          if (actorRef.current) {
            frame.contentWindow?.postMessage({ type: "artifact-hello", actor: actorRef.current }, "*");
          }
          return;
        case "state":
          if (msg.key === lastSentRef.current) return; // our own echo
          lastSentRef.current = msg.key;
          onState?.(msg.state);
          return;
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [onState]);

  // A new frame element (version/content change remounts it) gets its own
  // one-time hand-off. Stable identity, so a re-render does not detach and
  // re-attach the same frame (which would re-arm the hand-off).
  const attachFrame = useCallback((el: HTMLIFrameElement | null) => {
    if (el !== frameRef.current) handedOffRef.current = false;
    frameRef.current = el;
  }, []);

  if (!running) {
    return (
      <button
        type="button"
        onClick={() => setRunning(true)}
        className="tactile mt-1 flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-2.5 py-1.5 text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        title="Run this artifact in a sandboxed preview"
      >
        <PlayIcon size={13} className="text-[var(--color-accent)]" />
        Run live preview
      </button>
    );
  }

  const fill = height === "fill";
  return (
    <div className={fill ? "relative h-full overflow-hidden" : "relative mt-1 overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]"}>
      <iframe
        key={`${artifactId}:${version}:${contentKey(src)}:${reloadToken}`}
        ref={attachFrame}
        title="artifact preview"
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        src={runnerUrl()}
        style={{ height: fill ? "100%" : height, width: "100%", border: "0", display: "block", background: "#fff" }}
      />
      {!fill && (
      <button
        type="button"
        onClick={() => setRunning(false)}
        className="tactile absolute right-1.5 top-1.5 z-10 flex items-center gap-1 rounded-[var(--radius)] bg-[var(--color-surface)] px-1.5 py-0.5 text-[10px] text-[var(--color-fg-muted)] shadow"
        title="Stop the preview"
      >
        <StopIcon size={10} /> stop
      </button>
      )}
    </div>
  );
}
