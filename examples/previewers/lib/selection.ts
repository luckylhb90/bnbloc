// What the member points at in an Office previewer (preview.selection,
// docs/prd/2026-10-14-ai-document-editing.md M2): a part of the file they
// clicked (a slide, a paragraph, a table, a page) or text they selected in
// it. Each previewer says what its parts are; this module owns the label
// rules and the DOM plumbing so the previewers read alike. The sheet's cell
// ranges are drawn by the xlsx previewer itself (xlsx/src/main.ts).

import { sendSelection, type SelectionDraft, type SelectionKind } from "../../../web/previewer-sdk/src/index.ts";

/** The class a picked part wears (each previewer's style.css outlines it). */
export const PICKED = "bnbloc-picked";

const QUOTE_LIMIT = 48;
// A press that moves further than this is a drag or a scroll, not a click.
const CLICK_SLOP_PX = 4;

export function collapseText(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/** A part's text as the agent reads it: trimmed lines, no blank ones, a
 *  bullet the renderer set on its own line joined to its item. */
export function blockText(text: string): string {
  const out: string[] = [];
  let bullet = "";
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    if (/^[•◦▪▫‣⁃●○■□–-]$/.test(line)) {
      bullet = `${line} `;
      continue;
    }
    out.push(bullet + line);
    bullet = "";
  }
  return out.join("\n");
}

/** A table as tab-separated rows (a cell's lines folded). */
export function tableText(table: HTMLTableElement): string {
  return [...table.rows].map((row) => [...row.cells].map((cell) => collapseText(cell.innerText)).join("\t")).join("\n");
}

/** "Slide 3: “Q3 revenue grew…”": where, then the start of the text. */
export function quotedLabel(where: string, text: string): string {
  const flat = collapseText(text);
  if (!flat) return where;
  const chars = [...flat];
  const quote = chars.length > QUOTE_LIMIT ? `${chars.slice(0, QUOTE_LIMIT - 1).join("").trimEnd()}…` : flat;
  return where ? `${where}: “${quote}”` : `“${quote}”`;
}

/** A part of the file an element belongs to. */
export interface Part {
  readonly element: HTMLElement;
  /** Where it is ("Slide 3", "Page 2"). */
  readonly where: string;
  /** Its text (the selection's detail when clicked). */
  readonly text: string;
  /** The chip label when clicked (default: where and the start of text). */
  readonly label?: string;
  /** What it is (the host offers suggestions for the kind). */
  readonly kind: SelectionKind;
  /** The Compare unit keys it covers (a slide's), when it has any. */
  readonly keys?: readonly string[];
}

/** Reports a selection with where it shows, and reports it again as the
 *  frame scrolls or resizes, so the host's selection action stays beside
 *  it (docs/prd/2026-10-14-select-edit-review.md rule 3). */
export function selectionReporter(): { set(draft: Omit<SelectionDraft, "rect">, at: () => DOMRect | null): void; clear(): void } {
  let current: { draft: Omit<SelectionDraft, "rect">; at: () => DOMRect | null } | null = null;
  let frame = 0;
  const send = () => {
    if (!current) return;
    const r = current.at();
    sendSelection({ ...current.draft, ...(r ? { rect: { x: r.left, y: r.top, w: r.width, h: r.height } } : {}) });
  };
  const again = () => {
    if (!current || frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      send();
    });
  };
  document.addEventListener("scroll", again, { capture: true, passive: true });
  window.addEventListener("resize", again);
  return {
    set(draft, at) {
      current = { draft, at };
      send();
    },
    clear() {
      if (!current) return;
      current = null;
      sendSelection(null);
    },
  };
}

/** Reports the member's pointing inside root: text they select (named by
 *  the part it starts in), else the part they click. Clicking the picked
 *  part again, a click outside any part and Escape clear it. reset()
 *  clears it for a new file. */
export function watchPointing(root: HTMLElement, partOf: (el: Element) => Part | null): { reset(): void } {
  let picked: HTMLElement | null = null;
  let down: { x: number; y: number } | null = null;
  const report = selectionReporter();

  const mark = (el: HTMLElement | null) => {
    picked?.classList.remove(PICKED);
    picked = el;
    picked?.classList.add(PICKED);
  };
  const clear = () => {
    if (!picked) return;
    mark(null);
    report.clear();
  };

  root.addEventListener("pointerdown", (e) => {
    down = e.button === 0 ? { x: e.clientX, y: e.clientY } : null;
  });
  root.addEventListener("pointerup", (e) => {
    const start = down;
    down = null;
    if (!start || !(e.target instanceof Element)) return;
    const sel = document.getSelection();
    const text = sel && !sel.isCollapsed ? sel.toString().trim() : "";
    const anchor = sel?.anchorNode instanceof Element ? sel.anchorNode : sel?.anchorNode?.parentElement;
    if (sel && text && anchor && root.contains(anchor)) {
      const part = partOf(anchor);
      const range = sel.getRangeAt(0).cloneRange();
      mark(part?.element ?? null);
      report.set({ label: quotedLabel(part?.where ?? "", text), detail: text, kind: "text", ...(part?.keys ? { keys: part.keys } : {}) }, () => range.getBoundingClientRect());
      return;
    }
    if (Math.hypot(e.clientX - start.x, e.clientY - start.y) > CLICK_SLOP_PX) return;
    const part = partOf(e.target);
    if (!part || part.element === picked) {
      clear();
      return;
    }
    mark(part.element);
    const el = part.element;
    report.set(
      { label: part.label ?? quotedLabel(part.where, part.text), detail: blockText(part.text), kind: part.kind, ...(part.keys ? { keys: part.keys } : {}) },
      () => el.getBoundingClientRect(),
    );
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") clear();
  });
  return {
    reset() {
      picked = null;
      report.clear();
      sendSelection(null);
    },
  };
}

/** The class a revealed part wears for a moment (style.css animates it). */
export const FLASH = "bnbloc-flash";

/** Scrolls a part into view and marks it for a moment (a host's "Show"). */
export function reveal(el: Element | null | undefined): void {
  if (!el) return;
  el.scrollIntoView({ block: "center" });
  el.classList.remove(FLASH);
  void (el as HTMLElement).offsetWidth;
  el.classList.add(FLASH);
  setTimeout(() => el.classList.remove(FLASH), 1600);
}

/** Holds a reveal until the file is shown: apply() once it is. */
export function pendingReveal(show: (at: string) => boolean): { ask(at: string): void; apply(): void } {
  let at: string | null = null;
  return {
    ask(next) {
      at = next;
      if (show(next)) at = null;
    },
    apply() {
      if (at !== null && show(at)) at = null;
    },
  };
}
