import { apiUrl } from "@/lib/config";
import { runInHiddenFrame } from "@/lib/hiddenFrame";
import { previewerFrameUrl, readPreviewerMessage, type ConvertFileSrc } from "@/lib/previewerFrame";
import { freshPreviewer } from "../remotePreviewers";
import type { CompareStrategy, UnitList } from "./registry";

// An installed previewer that reads its format into units (capability
// "units", docs/prd/2026-10-14-file-prompts-and-compare.md rule 7): Compare
// asks it, in a hidden frame with the same origin, CSP and ticket as a
// visible one, to read each version. "How to read a pptx" lives in the
// pptx previewer only.

let seq = 0;

export function previewerUnits(name: string): CompareStrategy {
  return {
    kind: "units",
    async extract({ blob, file }, signal) {
      const desc = await freshPreviewer(name);
      if (!desc) throw new Error(`The ${name} previewer is no longer installed`);
      const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
      const src = previewerFrameUrl(desc, {
        convertFileSrc: internals?.convertFileSrc,
        apiUrl,
        baseURI: document.baseURI,
        hostOrigin: window.location.origin,
      });
      const bytes = await blob.arrayBuffer();
      const id = `u${Date.now().toString(36)}${(seq++).toString(36)}`;
      let asked = false;
      return runInHiddenFrame<UnitList>(
        src,
        (ev, frame, settle) => {
          const msg = readPreviewerMessage(ev, frame.window);
          if (!msg) return;
          if (msg.type === "preview.ready" && !asked) {
            asked = true;
            const meta = { name: file.name, mimeType: file.mimeType, sizeBytes: bytes.byteLength, version: file.version };
            frame.post({ type: "preview.units.request", id, file: meta, bytes }, [bytes]);
          } else if (msg.type === "preview.units" && msg.id === id) {
            if ("error" in msg) settle.reject(new Error(`${desc.name}: ${msg.error}`));
            else settle.resolve({ units: msg.units, truncated: msg.truncated });
          }
        },
        { signal, title: `Reading ${file.name}` },
      );
    },
  };
}
