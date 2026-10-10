import { type ReactNode } from "react";

// A tiny, dependency-free syntax highlighter for fenced code blocks. It is not a
// full parser — it tokenizes the few categories that carry most of the visual
// signal (comments, strings, numbers, and a shared keyword set across the
// languages agents emit: JS/TS/Go/Python/Rust/shell/JSON). Unknown languages
// still get string/number/comment coloring. Output is React spans using theme
// tokens, so there is no dependency and no XSS surface.

const KEYWORDS: Record<string, true> = {
  // control / declaration keywords common across our target languages
  const: true, let: true, var: true, function: true, func: true, fn: true,
  return: true, if: true, else: true, for: true, while: true, switch: true,
  case: true, break: true, continue: true, import: true, from: true,
  export: true, default: true, class: true, struct: true, interface: true,
  type: true, enum: true, extends: true, implements: true, new: true,
  async: true, await: true, yield: true, try: true, catch: true, finally: true,
  throw: true, def: true, lambda: true, pass: true, with: true, as: true,
  in: true, of: true, is: true, not: true, and: true, or: true, package: true,
  map: true, range: true, go: true, defer: true, chan: true, select: true,
  pub: true, use: true, mut: true, impl: true, trait: true, match: true,
  where: true, self: true, super: true, static: true, void: true, public: true,
  private: true, protected: true, this: true, typeof: true, instanceof: true,
};

const LITERALS: Record<string, true> = {
  true: true, false: true, null: true, undefined: true, nil: true, None: true,
  True: true, False: true, NaN: true, Infinity: true,
};

interface Tok {
  cls: string | null;
  text: string;
}

// tokenizeLine splits one line into colored tokens. Multi-line constructs
// (block comments) are handled by a carry flag from the caller.
function tokenizeLine(line: string, inBlockComment: boolean): { toks: Tok[]; inBlockComment: boolean } {
  const toks: Tok[] = [];
  let i = 0;
  let block = inBlockComment;
  const n = line.length;

  const pushRest = (cls: string) => {
    toks.push({ cls, text: line.slice(i) });
    i = n;
  };

  while (i < n) {
    if (block) {
      const end = line.indexOf("*/", i);
      if (end === -1) {
        pushRest("comment");
      } else {
        toks.push({ cls: "comment", text: line.slice(i, end + 2) });
        i = end + 2;
        block = false;
      }
      continue;
    }
    const c = line[i];
    const two = line.slice(i, i + 2);

    // line comments: // or # (shell/python/yaml) or --
    if (two === "//" || c === "#" || two === "--") {
      pushRest("comment");
      continue;
    }
    // block comment start
    if (two === "/*") {
      const end = line.indexOf("*/", i + 2);
      if (end === -1) {
        pushRest("comment");
        block = true;
      } else {
        toks.push({ cls: "comment", text: line.slice(i, end + 2) });
        i = end + 2;
      }
      continue;
    }
    // strings: ' " `
    if (c === '"' || c === "'" || c === "`") {
      let j = i + 1;
      while (j < n && line[j] !== c) {
        if (line[j] === "\\") j++;
        j++;
      }
      toks.push({ cls: "string", text: line.slice(i, Math.min(n, j + 1)) });
      i = j + 1;
      continue;
    }
    // numbers
    if (/[0-9]/.test(c)) {
      let j = i;
      while (j < n && /[0-9a-fA-FxX._]/.test(line[j])) j++;
      toks.push({ cls: "number", text: line.slice(i, j) });
      i = j;
      continue;
    }
    // identifiers / keywords
    if (/[A-Za-z_$]/.test(c)) {
      let j = i;
      while (j < n && /[A-Za-z0-9_$]/.test(line[j])) j++;
      const word = line.slice(i, j);
      const cls = KEYWORDS[word] ? "keyword" : LITERALS[word] ? "literal" : null;
      toks.push({ cls, text: word });
      i = j;
      continue;
    }
    // default: accumulate a run of "plain" chars until the next interesting one
    let j = i + 1;
    while (j < n && !/["'`#0-9A-Za-z_$]/.test(line[j]) && line.slice(j, j + 2) !== "//" && line.slice(j, j + 2) !== "/*" && line.slice(j, j + 2) !== "--") {
      j++;
    }
    toks.push({ cls: null, text: line.slice(i, j) });
    i = j;
  }
  return { toks, inBlockComment: block };
}

const CLASS_STYLE: Record<string, string> = {
  comment: "text-[var(--color-fg-subtle)] italic",
  string: "text-[var(--color-accent)]",
  number: "text-[var(--color-warning)]",
  keyword: "text-[var(--color-accent)] font-medium",
  literal: "text-[var(--color-warning)]",
};

// highlightLines returns each line's colored tokens (no newline nodes), with
// block comments carried across lines — the code viewer renders one row per
// line (line numbers, wrapping) from it.
export function highlightLines(code: string): ReactNode[][] {
  const lines = code.split("\n");
  const out: ReactNode[][] = [];
  let block = false;
  for (let li = 0; li < lines.length; li++) {
    const { toks, inBlockComment } = tokenizeLine(lines[li], block);
    block = inBlockComment;
    out.push(
      toks.map((t, ti) =>
        t.cls ? (
          <span key={ti} className={CLASS_STYLE[t.cls]}>
            {t.text}
          </span>
        ) : (
          t.text
        ),
      ),
    );
  }
  return out;
}

// highlight returns colored React nodes for a code block. Plain (unstyled)
// languages still benefit from string/number/comment coloring.
export function highlight(code: string): ReactNode[] {
  const lines = highlightLines(code);
  return lines.map((toks, li) => (
    <span key={li}>
      {toks}
      {li < lines.length - 1 ? "\n" : null}
    </span>
  ));
}
