import { useEffect, useRef, type RefObject } from "react";
import { fitSelection } from "../../../previewer-sdk/src/protocol.ts";
import { partsFromRules, watchPointing, type PartRule, type PointingOptions } from "../../../previewer-sdk/src/pointing.ts";
import type { ViewerSelection } from "./registry";

// How a built-in viewer points (docs/prd/2026-10-16-ask-ai-every-file.md
// rule 3): the SDK's pointing engine, the same one a previewer frame runs,
// reporting to the tab's onSelection. A viewer passes its part rules (data,
// kept next to it, stable between renders) and, for text, where a range is;
// the tab shows the action beside the rect, which is read in the tab's
// viewer box (the element marked data-viewer-box), not in the page.

/** Links, buttons and form controls inside a viewer are never parts. */
export const NOT_PARTS = "a[href], button, input, select, textarea, summary, label, [data-no-point]";

export type PointingHooks = Pick<PointingOptions, "describeText" | "markText">;

/** A viewer's pointing: text the member selects and the parts its rules name.
 *  `rules` and `hooks` must be stable (module constants or memoized). */
export function usePointing(
  rootRef: RefObject<HTMLElement | null>,
  onSelection: ((selection: ViewerSelection | null) => void) | undefined,
  rules: readonly PartRule[],
  hooks?: PointingHooks,
): void {
  const report = useRef(onSelection);
  report.current = onSelection;
  const wants = onSelection !== undefined;
  useEffect(() => {
    const root = rootRef.current;
    if (!root || !wants) return;
    const box = root.closest("[data-viewer-box]");
    const watch = watchPointing(
      root,
      partsFromRules(root, rules),
      (draft) => report.current?.(draft ? fitSelection(draft) : null),
      {
        ...hooks,
        ignore: NOT_PARTS,
        markText: hooks?.markText ?? false,
        place: (r) => {
          if (!box) return r;
          const b = box.getBoundingClientRect();
          return new DOMRect(r.left - b.left, r.top - b.top, r.width, r.height);
        },
      },
    );
    return () => {
      watch.stop();
      report.current?.(null);
    };
  }, [rootRef, wants, rules, hooks]);
}
