import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { isPreviewRoom } from "@/lib/apiModels";
import { ApiError, errorText } from "@/lib/errors";
import { gatewayBase } from "@/lib/config";
import { localFiles, type LocalOpened } from "@/lib/localFiles";
import { MY_FILES_DESCRIPTION, MY_FILES_NAME, myFilesRoomId, openLocal, type OpenDeps } from "@/lib/localOpen";
import { StorageKey, memberKey, readStored, writeStored } from "@/lib/storageKeys";
import { artifactLinks, fileAskRequests } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";

// LocalFileOpener opens the member's local documents in bnbloc (desktop
// only; docs/prd/2026-10-14-ai-document-editing.md §4, M3): the ones the OS
// hands the app (Open With, a double-click on an associated type,
// `bnbloc-desktop <file>`) and the ones picked through "Open a file…"
// (requestLocalOpen, ⌘O). The first time, it says the open uploads a copy;
// then each lands, as a room file, in a workbench tab.

const OPEN_REQUEST = "bnbloc:local-open";

/** Shows the Open dialog (the sidebar's "Open a file…"). */
export function requestLocalOpen(): void {
  window.dispatchEvent(new Event(OPEN_REQUEST));
}

function liveDeps(gateway: string, tenant: string, subject: string): OpenDeps {
  return {
    gateway,
    ensureRoom: async () => {
      const id = await myFilesRoomId(tenant, subject);
      try {
        const room = await api.getRoom(id);
        if (!isPreviewRoom(room)) return id;
        throw new Error("Your My files room is not available");
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 404)) throw e;
      }
      try {
        await api.createRoom({ roomId: id, name: MY_FILES_NAME, description: MY_FILES_DESCRIPTION, visibility: "private" });
      } catch (e) {
        // Another window created it first.
        if (!(e instanceof ApiError && e.status === 409)) throw e;
      }
      return id;
    },
    read: (token) => localFiles.read(token),
    bind: (token, binding) => localFiles.bind(token, binding),
    currentVersion: async (roomId, artifactId) => {
      try {
        return (await api.artifactDetail(roomId, artifactId)).artifact.currentVersion;
      } catch (e) {
        if (e instanceof ApiError && (e.status === 404 || e.status === 403)) return null;
        throw e;
      }
    },
    createFile: async (roomId, name) => (await api.createArtifact(roomId, { kind: "file", title: name, fileName: name })).artifact.artifactId,
    publish: async (roomId, artifactId, bytes, name, baseVersion, summary) => {
      const rep = await api.publishArtifactFile(roomId, artifactId, { file: new Blob([bytes]), fileName: name, baseVersion, summary });
      return rep.version?.version ?? rep.artifact.currentVersion;
    },
  };
}

export function LocalFileOpener({ subject, tenant, onOpenRoom }: { readonly subject: string; readonly tenant: string; readonly onOpenRoom: (roomId: string) => void }) {
  const toast = useToast();
  const [queue, setQueue] = useState<LocalOpened[]>([]);
  const [busy, setBusy] = useState(false);
  const [dontAsk, setDontAsk] = useState(false);
  const consentKey = memberKey(StorageKey.localOpenConsent, { gateway: gatewayBase(), subject });
  const consented = useRef(readStored(consentKey) === "1");

  const enqueue = useCallback((items: readonly LocalOpened[]) => {
    if (items.length > 0) setQueue((q) => [...q, ...items.filter((o) => !q.some((x) => x.token === o.token))]);
  }, []);

  // Documents the OS opened the app with: now (a cold start) and later.
  useEffect(() => {
    if (!localFiles.available()) return;
    let stop: (() => void) | undefined;
    let live = true;
    const drain = () => void localFiles.takePending().then((items) => live && enqueue(items), () => {});
    drain();
    void localFiles.onOpened(drain).then((s) => (live ? (stop = s) : s()));
    return () => {
      live = false;
      stop?.();
    };
  }, [enqueue]);

  // "Open a file…" and ⌘O / Ctrl+O.
  useEffect(() => {
    if (!localFiles.available()) return;
    const pick = () =>
      void localFiles.pick().then(
        (o) => o && enqueue([o]),
        (e: unknown) => toast.error(errorText(e, "Could not open the file")),
      );
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "o") {
        e.preventDefault();
        pick();
      }
    };
    window.addEventListener(OPEN_REQUEST, pick);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener(OPEN_REQUEST, pick);
      window.removeEventListener("keydown", onKey);
    };
  }, [enqueue, toast]);

  const head = queue[0];
  const open = useCallback(
    async (o: LocalOpened) => {
      setBusy(true);
      try {
        const at = await openLocal(o, liveDeps(gatewayBase(), tenant, subject));
        artifactLinks.queue(at.roomId, { artifactId: at.artifactId });
        // Its Ask AI panel opens with it: the member opened it to work on it.
        fileAskRequests.queue(at.roomId, { artifactId: at.artifactId });
        onOpenRoom(at.roomId);
      } catch (e) {
        toast.error(errorText(e, `Could not open ${o.name}`));
      } finally {
        setBusy(false);
        setQueue((q) => q.filter((x) => x.token !== o.token));
      }
    },
    [tenant, subject, onOpenRoom, toast],
  );

  // A bound document goes back to its room file: no consent to ask again.
  const needsConsent = !!head && !consented.current && !head.binding;
  useEffect(() => {
    if (!head || busy || needsConsent) return;
    toast.info(`Opening ${head.name}…`);
    void open(head);
  }, [head, busy, needsConsent, open, toast]);

  if (!head || !needsConsent) return null;
  return (
    <Dialog
      open
      size="sm"
      onClose={() => setQueue((q) => q.slice(1))}
      title={`Open ${head.name}?`}
      description="Opening uploads a copy to your organization's bnbloc, so your agent can work on it. It goes to your private My files room; the original stays where it is, and you choose when to save a version back to it."
      footer={
        <>
          <label className="mr-auto flex items-center gap-2 text-xs text-[var(--color-fg-muted)]">
            <input type="checkbox" checked={dontAsk} onChange={(e) => setDontAsk(e.target.checked)} />
            Don't ask again
          </label>
          <Button variant="ghost" onClick={() => setQueue((q) => q.slice(1))}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={() => {
              if (dontAsk) writeStored(consentKey, "1");
              consented.current = dontAsk;
              void open(head);
            }}
          >
            {busy ? "Opening…" : "Open"}
          </Button>
        </>
      }
    />
  );
}
