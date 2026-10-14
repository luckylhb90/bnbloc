import { composeArtifactDocument, loadMessage } from "@/lib/artifactFrame";
import { runInHiddenFrame } from "@/lib/hiddenFrame";
import { loadPdfjs, pdfRunnerUrl } from "../pdfAssets";
import { PDF_GUEST_HTML, readPdfMessage } from "../pdfFrame";
import type { CompareStrategy, UnitList } from "./registry";

// A PDF compared page by page: the PDF guest (pdfFrame.ts) reads each page's
// text in a hidden runner frame, the same sandbox the viewer uses; the app
// never parses the PDF. Pages carry no identity, so they align by text.

export const pdfUnits: CompareStrategy = {
  kind: "units",
  noun: ["page", "pages"],
  async extract({ blob, file }, signal) {
    const [sources, bytes] = await Promise.all([loadPdfjs(), blob.arrayBuffer()]);
    const doc = composeArtifactDocument(PDF_GUEST_HTML);
    let loaded = false;
    return runInHiddenFrame<UnitList>(
      pdfRunnerUrl(),
      (ev, frame, settle) => {
        const msg = readPdfMessage(ev, frame.window);
        if (!msg) return;
        if (msg.kind === "runner-ready") frame.post(loadMessage(doc));
        else if (msg.kind === "ready" && !loaded) {
          loaded = true;
          frame.post({ type: "pdf.units", lib: sources.lib, worker: sources.worker, bytes }, [bytes]);
        } else if (msg.kind === "units") settle.resolve({ units: msg.units, truncated: msg.truncated });
        else if (msg.kind === "error") settle.reject(new Error(`${file.name}: ${msg.message}`));
      },
      { signal, title: `Reading ${file.name}` },
    );
  },
};
