import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { artifactRunnerUrl, composeArtifactDocument, loadMessage, type ConvertFileSrc } from "@/lib/artifactFrame";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { PDF_GUEST_HTML, readPdfMessage } from "./pdfFrame";

// PdfViewer hosts pdf.js INSIDE the sandboxed artifact runner frame (see
// pdfFrame.ts): the app origin never parses the PDF. pdf.js (library +
// worker, ~1.8 MB raw / ~0.5 MB gzip) is a lazy chunk fetched only when a
// PDF opens, as source text handed to the frame — the frame itself loads
// nothing from the network.

function runnerUrl(): string {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return artifactRunnerUrl({ convertFileSrc: internals?.convertFileSrc, baseURI: document.baseURI, hostOrigin: window.location.origin });
}

let pdfjsSources: Promise<{ lib: string; worker: string }> | null = null;

function loadPdfjs(): Promise<{ lib: string; worker: string }> {
  pdfjsSources ??= Promise.all([
    import("pdfjs-dist/legacy/build/pdf.min.mjs?raw"),
    import("pdfjs-dist/legacy/build/pdf.worker.min.mjs?raw"),
  ]).then(([lib, worker]) => ({ lib: lib.default, worker: worker.default }));
  pdfjsSources.catch(() => {
    pdfjsSources = null;
  });
  return pdfjsSources;
}

export default function PdfViewer({ blob, file, toolbar }: ArtifactViewerProps) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const handedOff = useRef(false);
  const loaded = useRef(false);
  const [status, setStatus] = useState({ page: 0, pages: 0, zoom: 1 });
  const [error, setError] = useState("");
  const doc = useMemo(() => composeArtifactDocument(PDF_GUEST_HTML), []);
  const src = useMemo(runnerUrl, []);

  const send = useCallback((message: unknown, transfer?: Transferable[]) => {
    frameRef.current?.contentWindow?.postMessage(message, "*", transfer);
  }, []);

  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const msg = readPdfMessage(ev, frameRef.current?.contentWindow);
      if (!msg) return;
      switch (msg.kind) {
        case "runner-ready":
          if (handedOff.current) return;
          handedOff.current = true;
          // A sandboxed frame's origin is opaque: "*" is the only target the
          // platform accepts; the source check above pins the frame.
          send(loadMessage(doc));
          return;
        case "ready":
          if (loaded.current) return;
          loaded.current = true;
          void Promise.all([loadPdfjs(), blob.arrayBuffer()])
            .then(([sources, bytes]) => {
              const background = getComputedStyle(document.documentElement).getPropertyValue("--color-bg").trim();
              send({ type: "pdf.load", lib: sources.lib, worker: sources.worker, bytes, background }, [bytes]);
            })
            .catch(() => setError("Could not load the PDF viewer."));
          return;
        case "status":
          setStatus({ page: msg.page, pages: msg.pages, zoom: msg.zoom });
          return;
        case "error":
          setError(msg.message);
          return;
        default:
          return;
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [blob, doc, send]);

  useViewerToolbar(
    toolbar,
    [
      { id: "zoom", kind: "zoom", icon: "zoom-in", label: "Zoom", value: status.zoom, disabled: status.pages === 0 },
      { id: "fit", kind: "button", icon: "fit", label: "Fit width", disabled: status.pages === 0 },
      { id: "page-prev", kind: "button", icon: "page-prev", label: "Previous page", disabled: status.page <= 1 },
      { id: "page-next", kind: "button", icon: "page-next", label: "Next page", disabled: status.page >= status.pages },
    ],
    (id, value) => send({ type: "pdf.command", id, value }),
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <iframe
        ref={frameRef}
        title={`${file.name} (PDF)`}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        src={src}
        className="min-h-0 w-full flex-1 border-0"
        style={{ background: "var(--color-bg)" }}
      />
      <div className="flex shrink-0 items-center gap-2 border-t border-[var(--color-border)] px-3 py-1 text-[11px] text-[var(--color-fg-subtle)] tabular">
        {error ? (
          <span className="text-[var(--color-danger,#f87171)]">{error}</span>
        ) : status.pages > 0 ? (
          <span>
            Page {status.page} / {status.pages}
          </span>
        ) : (
          <span>Loading…</span>
        )}
        <span className="ml-auto">{Math.round(status.zoom * 100)}%</span>
      </div>
    </div>
  );
}
