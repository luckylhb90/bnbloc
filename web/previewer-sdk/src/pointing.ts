// What the member points at in a viewer (docs/prd/2026-10-16-ask-ai-every-file.md
// rule 2): text they select, or the part of the file they click, becomes a
// selection the Ask AI panel names. This is the one engine: a previewer frame
// (its report is the SDK's sendSelection) and a built-in viewer (its report is
// the tab's onSelection) use the same code, so a click means the same
// everywhere. A viewer says what its parts are (partOf, the part rules it
// builds, usually as data) and, for text, where a range is (describeText);
// this module owns the click-versus-drag rule, the chip label, the bounded
// detail and re-reporting the rect as the view moves.
//
// DOM only (no framework, no frame protocol); the pure helpers are
// unit-tested under node.

import type { fitSelection, SelectionKind } from "./protocol.ts";

/** A selection as a viewer reports it (the protocol bounds it). */
export type PointingDraft = Parameters<typeof fitSelection>[0];

/** Where a selection goes: the SDK's sendSelection in a frame, the tab's
 *  onSelection in the app. null clears it. */
export type SelectionReport = (selection: PointingDraft | null) => void;

/** The class a picked part wears (each viewer's CSS outlines it). */
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

/** A kind of part as data: which elements are one, what to call it, what it
 *  covers. A viewer is a list of these (partsFromRules turns them into the
 *  engine's partOf); adding a kind of part is adding a row. */
export interface PartRule {
  /** The elements that are this part (the innermost one a click lands in
   *  wins; among rules matching the same element, the first listed). */
  readonly selector: string;
  /** What it is ("Heading", "Paragraph"), or a function of the element. */
  readonly noun: string | ((el: HTMLElement) => string);
  readonly kind: SelectionKind;
  /** Its text; default: the element's visible text. */
  readonly text?: (el: HTMLElement) => string;
  /** The chip label; default: the noun and the start of the text. */
  readonly label?: (el: HTMLElement, text: string) => string;
  /** The unit keys it covers. */
  readonly keys?: (el: HTMLElement) => readonly string[] | undefined;
  /** The element to outline when a click on the matched one picks it
   *  (a line's number picks the whole line). */
  readonly element?: (el: HTMLElement) => HTMLElement;
}

/** The engine's partOf for a list of rules: walks up from the clicked
 *  element to root and takes the first element a rule matches. */
export function partsFromRules(root: HTMLElement, rules: readonly PartRule[]): (el: Element) => Part | null {
  return (start) => {
    for (let el: Element | null = start; el && el !== root.parentElement; el = el.parentElement) {
      if (!(el instanceof HTMLElement)) continue;
      const rule = rules.find((r) => el.matches(r.selector));
      if (!rule) {
        if (el === root) break;
        continue;
      }
      const text = rule.text ? rule.text(el) : el.innerText;
      const where = typeof rule.noun === "function" ? rule.noun(el) : rule.noun;
      const keys = rule.keys?.(el);
      return {
        element: rule.element ? rule.element(el) : el,
        where,
        text,
        kind: rule.kind,
        ...(rule.label ? { label: rule.label(el, text) } : {}),
        ...(keys ? { keys } : {}),
      };
    }
    return null;
  };
}

/** Reports a selection with where it shows, and reports it again as the view
 *  scrolls or resizes, so the host's selection action stays beside it
 *  (docs/prd/2026-10-14-select-edit-review.md rule 3). */
export function selectionReporter(report: SelectionReport): {
  set(draft: Omit<PointingDraft, "rect">, at: () => DOMRect | null): void;
  clear(): void;
  /** Stops re-reporting (a viewer that is going away). */
  dispose(): void;
} {
  let current: { draft: Omit<PointingDraft, "rect">; at: () => DOMRect | null } | null = null;
  let frame = 0;
  const send = () => {
    if (!current) return;
    const r = current.at();
    report({ ...current.draft, ...(r ? { rect: { x: r.left, y: r.top, w: r.width, h: r.height } } : {}) });
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
      report(null);
    },
    dispose() {
      current = null;
      cancelAnimationFrame(frame);
      frame = 0;
      document.removeEventListener("scroll", again, { capture: true });
      window.removeEventListener("resize", again);
    },
  };
}

export interface PointingOptions {
  /** Says where a range of selected text is ("Lines 12–18", "Page 3"),
   *  instead of the part it starts in. null falls back to that part. */
  readonly describeText?: (range: Range, anchor: Element) => { readonly where: string; readonly keys?: readonly string[] } | null;
  /** Elements that are never parts (links, buttons, toggles), as a selector. */
  readonly ignore?: string;
  /** Whether selected text also outlines the part it starts in (default
   *  true); a viewer that shows the selection itself says false. */
  readonly markText?: boolean;
  /** Maps a rect from the page to where the report is read: a frame reports
   *  in its own viewport (the default), a built-in viewer in the tab's box. */
  readonly place?: (rect: DOMRect) => DOMRect;
}

/** Reports the member's pointing inside root: text they select (named by
 *  describeText or the part it starts in), else the part they click. Clicking
 *  the picked part again, a click outside any part and Escape clear it.
 *  reset() clears it for a new file; stop() detaches it. */
export function watchPointing(
  root: HTMLElement,
  partOf: (el: Element) => Part | null,
  report: SelectionReport,
  options: PointingOptions = {},
): { reset(): void; stop(): void } {
  let picked: HTMLElement | null = null;
  let down: { x: number; y: number } | null = null;
  const reporter = selectionReporter(report);
  const place = options.place ?? ((r: DOMRect) => r);
  const markText = options.markText ?? true;

  const mark = (el: HTMLElement | null) => {
    picked?.classList.remove(PICKED);
    picked = el;
    picked?.classList.add(PICKED);
  };
  const clear = () => {
    if (!picked && !textPicked) return;
    mark(null);
    textPicked = false;
    reporter.clear();
  };
  // A text selection with nothing outlined still needs Escape to clear it.
  let textPicked = false;

  const onDown = (e: PointerEvent) => {
    down = e.button === 0 ? { x: e.clientX, y: e.clientY } : null;
  };
  const onUp = (e: PointerEvent) => {
    const start = down;
    down = null;
    if (!start || !(e.target instanceof Element)) return;
    if (options.ignore && e.target.closest(options.ignore)) return;
    const sel = document.getSelection();
    const text = sel && !sel.isCollapsed ? sel.toString().trim() : "";
    const anchor = sel?.anchorNode instanceof Element ? sel.anchorNode : sel?.anchorNode?.parentElement;
    if (sel && text && anchor && root.contains(anchor)) {
      const range = sel.getRangeAt(0).cloneRange();
      const part = partOf(anchor);
      const described = options.describeText?.(range, anchor) ?? null;
      mark(markText ? (part?.element ?? null) : null);
      textPicked = true;
      const keys = described?.keys ?? part?.keys;
      reporter.set(
        { label: quotedLabel(described?.where ?? part?.where ?? "", text), detail: text, kind: "text", ...(keys ? { keys } : {}) },
        () => place(range.getBoundingClientRect()),
      );
      return;
    }
    if (Math.hypot(e.clientX - start.x, e.clientY - start.y) > CLICK_SLOP_PX) return;
    const part = partOf(e.target);
    if (!part || part.element === picked) {
      clear();
      return;
    }
    mark(part.element);
    textPicked = false;
    const el = part.element;
    reporter.set(
      { label: part.label ?? quotedLabel(part.where, part.text), detail: blockText(part.text), kind: part.kind, ...(part.keys ? { keys: part.keys } : {}) },
      () => place(el.getBoundingClientRect()),
    );
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") clear();
  };
  root.addEventListener("pointerdown", onDown);
  root.addEventListener("pointerup", onUp);
  document.addEventListener("keydown", onKey);
  return {
    reset() {
      picked?.classList.remove(PICKED);
      picked = null;
      textPicked = false;
      reporter.clear();
      report(null);
    },
    stop() {
      root.removeEventListener("pointerdown", onDown);
      root.removeEventListener("pointerup", onUp);
      document.removeEventListener("keydown", onKey);
      mark(null);
      if (textPicked) reporter.clear();
      textPicked = false;
      reporter.dispose();
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
