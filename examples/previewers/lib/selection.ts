// What the member points at in a previewer (preview.selection): a part of the
// file they clicked (a slide, a paragraph, a table, a page) or text they
// selected in it. The engine is the SDK's (web/previewer-sdk/src/pointing.ts,
// shared with the app's built-in viewers); this module binds it to the
// frame's report, sendSelection, so a previewer calls watchPointing(root,
// partOf) and nothing else. The sheet's cell ranges are drawn by the xlsx
// previewer itself (xlsx/src/main.ts).

import { sendSelection } from "../../../web/previewer-sdk/src/index.ts";
import { selectionReporter as engineReporter, watchPointing as enginePointing, type Part } from "../../../web/previewer-sdk/src/pointing.ts";

export { FLASH, PICKED, blockText, collapseText, pendingReveal, quotedLabel, reveal, tableText, type Part } from "../../../web/previewer-sdk/src/pointing.ts";

/** Reports a selection with where it shows (see the engine). */
export function selectionReporter() {
  return engineReporter(sendSelection);
}

/** Reports the member's pointing inside root (see the engine). */
export function watchPointing(root: HTMLElement, partOf: (el: Element) => Part | null): { reset(): void } {
  return enginePointing(root, partOf, sendSelection);
}
