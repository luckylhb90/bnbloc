import { useCallback, useEffect, useRef, useState, type ComponentType } from "react";
import { WarningCircleIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { previewerFrameUrl, readPreviewerMessage, saveFailure, saveRefusal, wantsThumbnail, type PreviewerDescriptor } from "@/lib/previewerFrame";
import { PREVIEWER_PROTOCOL, stateKey, type HostMessage } from "../../../previewer-sdk/src/protocol.ts";
import type { ArtifactViewerProps, ViewerToolbarItem } from "./registry";
import { freshPreviewer, installedPreviewer } from "./remotePreviewers";
import { frameEnv, frameTheme } from "./siteFrameEnv";
import { errorText } from "@/lib/errors";

// RemotePreviewer hosts an installed previewer component: a third-party
// static bundle rendering one artifact version inside a sandboxed frame.
//
// Security model (same as the artifact runner, ArtifactRunner.tsx):
//   - sandbox="allow-scripts" WITHOUT allow-same-origin: opaque origin, no
//     cookies/storage, no app DOM, no same-origin API calls, no Tauri IPC;
//   - the bundle is served (gateway frame route / desktop scheme) from the
//     sha256-pinned artifact with a CSP that refuses every network load:
//     the file reaches the frame only as bytes the host transfers;
//   - every inbound message is validated (origin "null", this frame's
//     window, known type, size limits) by lib/previewerFrame + the SDK's
//     protocol module; the frame drives only data — toolbar items by icon
//     NAME, live state into the room's artifact_state channel, a PNG
//     thumbnail the host keeps (onCover) when the previewer declared the
//     capability.

type Desc = PreviewerDescriptor;

const hosts = new Map<string, ComponentType<ArtifactViewerProps>>();

/** The registry component for one installed previewer (stable per name, so
 *  a catalog re-sync never remounts open frames). */
export function previewerHost(name: string): ComponentType<ArtifactViewerProps> {
  let host = hosts.get(name);
  if (!host) {
    host = function InstalledPreviewer(props: ArtifactViewerProps) {
      return <RemotePreviewer name={name} {...props} />;
    };
    hosts.set(name, host);
  }
  return host;
}

export function RemotePreviewer({ name, ...props }: ArtifactViewerProps & { readonly name: string }) {
  // Resolve a descriptor whose frame ticket is valid for a new frame.
  const [desc, setDesc] = useState<Desc | null | undefined>(() => installedPreviewer(name));
  useEffect(() => {
    let cancelled = false;
    void freshPreviewer(name)
      .then((d) => !cancelled && setDesc(d ?? null))
      .catch(() => !cancelled && setDesc(installedPreviewer(name) ?? null));
    return () => {
      cancelled = true;
    };
  }, [name]);
  if (desc === null) {
    return <PreviewerNotice text={`The ${name} previewer is no longer installed for you.`} />;
  }
  if (!desc) return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>;
  return <PreviewerFrame desc={desc} {...props} />;
}

function PreviewerNotice({ text }: { readonly text: string }) {
  return (
    <p className="flex items-center gap-2 p-4 text-sm text-[var(--color-fg-muted)]">
      <WarningCircleIcon size={16} className="shrink-0 text-[var(--color-danger,#f87171)]" /> {text}
    </p>
  );
}

function PreviewerFrame({
  desc,
  roomId,
  artifactId,
  version,
  file,
  blob,
  toolbar,
  onLocalState,
  remoteState,
  onSelection,
  reveal,
  onCover,
}: ArtifactViewerProps & { readonly desc: Desc }) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const readyRef = useRef(false);
  const lastSentState = useRef("");
  const remoteNonce = useRef(0);
  const thumbnailDone = useRef(false);
  // The version the frame was handed: a save is compare-and-swap on it, and
  // moves on after each save (the frame keeps the bytes it saved).
  const loadedVersion = useRef(0);
  const [error, setError] = useState("");
  // Until the frame says it is ready its bundle is still downloading: say so
  // (a large previewer on a slow link takes a while), never "no file".
  const [ready, setReady] = useState(false);
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    if (ready) return;
    const t = window.setTimeout(() => setSlow(true), 8000);
    return () => window.clearTimeout(t);
  }, [ready]);
  const [title, setTitle] = useState("");
  const liveState = desc.capabilities.includes("liveState");
  const selection = desc.capabilities.includes("selection");

  // Latest props for the message handler (registered once per frame).
  const units = desc.capabilities.includes("units");
  const latest = useRef({ blob, file, version, onLocalState, onSelection, onCover, roomId, artifactId, revealAt: reveal?.at });
  latest.current = { blob, file, version, onLocalState, onSelection, onCover, roomId, artifactId, revealAt: reveal?.at };

  const post = useCallback((msg: HostMessage, transfer?: Transferable[]) => {
    // A sandboxed frame's origin is opaque: "*" is the only accepted target;
    // the iframe element pins the recipient window.
    frameRef.current?.contentWindow?.postMessage(msg, "*", transfer ?? []);
  }, []);

  // The declared toolbar shows until the frame publishes its own.
  useEffect(() => {
    toolbar.set(desc.toolbar as readonly ViewerToolbarItem[]);
  }, [toolbar, desc]);
  useEffect(
    () =>
      toolbar.subscribe((id, value) => {
        if (!readyRef.current || typeof value === "string") return;
        post({ type: "preview.toolbar", id, value });
      }),
    [toolbar, post],
  );

  const handOff = useCallback(async () => {
    const { blob: bytes, file: info, version: v } = latest.current;
    loadedVersion.current = v;
    const box = boxRef.current;
    post({
      type: "preview.init",
      protocol: PREVIEWER_PROTOCOL,
      file: { name: info.name, mimeType: info.mimeType, sizeBytes: bytes.size, version: v },
      theme: frameTheme(),
      viewport: { w: box?.clientWidth ?? 0, h: box?.clientHeight ?? 0 },
    });
    const buffer = await bytes.arrayBuffer();
    post({ type: "preview.file", bytes: buffer }, [buffer]);
    const at = latest.current.revealAt;
    if (at && units) post({ type: "preview.reveal", at });
    if (liveState && latest.current.artifactId) {
      const snap = await api.getArtifactState(latest.current.roomId, latest.current.artifactId).catch(() => null);
      if (snap && stateKey(snap.state) !== null) post({ type: "preview.state", state: snap.state, actor: snap.actor });
    }
  }, [post, liveState, units]);

  // "Show" a unit: once the frame has the file (handOff sends a pending one).
  useEffect(() => {
    if (reveal && units && readyRef.current) post({ type: "preview.reveal", at: reveal.at });
    // A new nonce shows it again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal?.nonce]);

  // The frame's save (capability "edit"): publishes its bytes as the next
  // version of the file, compare-and-swap on the version the frame holds. The
  // answer goes back to the frame; nothing here throws into it.
  const saveScene = useCallback(
    async (bytes: ArrayBuffer, summary: string | undefined) => {
      const { roomId, artifactId, file: info } = latest.current;
      const refusal = saveRefusal(desc, { roomId, artifactId });
      if (refusal) {
        post({ type: "preview.save.failed", message: refusal });
        return;
      }
      try {
        const reply = await api.publishArtifactFile(roomId, artifactId, {
          file: new Blob([bytes], { type: info.mimeType }),
          fileName: info.name,
          baseVersion: loadedVersion.current,
          summary,
        });
        if (reply.version) loadedVersion.current = reply.version.version;
        post({ type: "preview.saved", version: loadedVersion.current });
      } catch (e: unknown) {
        post({ type: "preview.save.failed", message: saveFailure(e) });
      }
    },
    [desc, post],
  );

  const storeThumbnail = useCallback(
    (png: ArrayBuffer) => {
      if (thumbnailDone.current || !wantsThumbnail(desc)) return;
      thumbnailDone.current = true;
      latest.current.onCover?.(png, "previewer");
    },
    [desc],
  );

  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const msg = readPreviewerMessage(ev, frameRef.current?.contentWindow);
      if (!msg) return;
      switch (msg.type) {
        case "preview.ready":
          if (readyRef.current) return; // one hand-off per frame
          readyRef.current = true;
          setReady(true);
          void handOff().catch((e: unknown) => setError(errorText(e, "Could not load the file")));
          return;
        case "preview.toolbar.set":
          toolbar.set(msg.items as readonly ViewerToolbarItem[]);
          return;
        case "preview.state.send": {
          if (!liveState || !latest.current.artifactId) return;
          const key = stateKey(msg.state);
          if (key === null || key === lastSentState.current) return;
          lastSentState.current = key;
          latest.current.onLocalState?.(msg.state);
          return;
        }
        case "preview.thumbnail":
          storeThumbnail(msg.png);
          return;
        case "preview.error":
          setError(msg.message);
          return;
        case "preview.title":
          setTitle(msg.text);
          return;
        case "preview.selection":
          if (selection) latest.current.onSelection?.(msg.selection);
          return;
        case "preview.save":
          void saveScene(msg.bytes, msg.summary);
          return;
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [handOff, toolbar, liveState, selection, storeThumbnail, saveScene]);

  // Remote live state from the room, deduped by nonce.
  useEffect(() => {
    if (!liveState || !remoteState || !readyRef.current) return;
    if (remoteState.nonce === remoteNonce.current) return;
    remoteNonce.current = remoteState.nonce;
    if (stateKey(remoteState.state) === null) return;
    post({ type: "preview.state", state: remoteState.state, actor: remoteState.actor });
  }, [remoteState, liveState, post]);

  // Viewport changes.
  useEffect(() => {
    const box = boxRef.current;
    if (!box || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (readyRef.current) post({ type: "preview.resize", w: box.clientWidth, h: box.clientHeight });
    });
    observer.observe(box);
    return () => observer.disconnect();
  }, [post]);

  const src = previewerFrameUrl(desc, frameEnv());
  return (
    <div ref={boxRef} className="relative h-full min-h-[360px] w-full overflow-hidden">
      <iframe
        ref={frameRef}
        title={title || `${desc.name} preview of ${file.name}`}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        src={src}
        className="absolute inset-0 block h-full w-full border-0"
      />
      {!ready && !error && (
        <div className="absolute inset-0 grid place-items-center bg-[var(--color-bg)] p-4 text-center text-sm text-[var(--color-fg-subtle)]" role="status">
          <p>
            Loading the {desc.name} previewer…
            {slow && <span className="mt-1 block text-xs">It is still downloading; a slow connection can take a while.</span>}
          </p>
        </div>
      )}
      {error && (
        <div className="absolute inset-x-2 bottom-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-xs text-[var(--color-fg-muted)] shadow">
          <span className="font-medium text-[var(--color-fg)]">{desc.name}:</span> {error}
        </div>
      )}
    </div>
  );
}
