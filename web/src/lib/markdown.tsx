import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { useEffect, useId, useState, type MouseEvent } from "react";
import { openExternal } from "./desktop";
import { highlight } from "./highlight";
import { codeLanguage, isCodeBlock } from "./markdownCode";

const TABLE_SEPARATOR_CELL = /^:?-{2,}:?$/;
const TABLE_SEPARATOR_CELL_GLOBAL = /\|\s*:?-{2,}:?\s*(?=\|)/g;

function splitTableRow(row: string): string[] {
  return row
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split(/(?<!\\)\|/)
    .map((cell) => cell.trim().replace(/\\\|/g, "|"));
}

function findCrammedSeparatorStart(line: string): number | null {
  TABLE_SEPARATOR_CELL_GLOBAL.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = TABLE_SEPARATOR_CELL_GLOBAL.exec(line)) !== null) {
    const separatorStart = match.index;
    const separatorCells = splitTableRow(line.slice(separatorStart));
    const separatorCount = separatorCells.findIndex((cell) => !TABLE_SEPARATOR_CELL.test(cell));
    const headerCells = splitTableRow(line.slice(0, separatorStart));
    if (separatorCount >= 2 && separatorCount === headerCells.length) {
      return separatorStart;
    }
  }
  return null;
}

function expandCrammedTableLine(line: string): string[] | null {
  const separatorStart = findCrammedSeparatorStart(line);
  if (separatorStart === null) return null;

  const header = line.slice(0, separatorStart).trim();
  const restCells = splitTableRow(line.slice(separatorStart));
  const headerCellCount = splitTableRow(header).length;
  const separator = restCells.slice(0, headerCellCount).join(" | ");
  const bodyCells = restCells.slice(headerCellCount);
  const bodyRows: string[] = [];

  for (let i = 0; i < bodyCells.length; i += headerCellCount) {
    const rowCells = bodyCells.slice(i, i + headerCellCount);
    if (rowCells.length === headerCellCount) bodyRows.push(`| ${rowCells.join(" | ")} |`);
  }

  if (bodyRows.length === 0) return null;
  return [`| ${splitTableRow(header).join(" | ")} |`, `| ${separator} |`, ...bodyRows];
}

function normalizeCrammedTables(text: string): string {
  return text
    .split("\n")
    .flatMap((line) => expandCrammedTableLine(line) ?? [line])
    .join("\n");
}

// Mermaid renders untrusted, agent/member-authored diagram source, and its
// SVG is injected as markup. Defense in depth, both layers required:
//   1. mermaid runs with securityLevel "strict" (set explicitly, never relying
//      on a library default): labels are HTML-escaped and click/callback
//      directives are disabled;
//   2. the produced SVG is sanitized with DOMPurify (svg + svgFilters + html
//      profiles, for foreignObject labels) before it touches the DOM, which
//      strips <script>, on* handlers and javascript: URLs even if a mermaid
//      bug lets one through. DOMPurify is already shipped inside mermaid's own
//      dependency tree; it is declared directly so this guarantee does not
//      hinge on a transitive dependency, and the lazily loaded diagram chunk
//      dedupes onto the same copy.
let mermaidReady: Promise<typeof import("mermaid").default> | null = null;
function loadMermaid() {
  mermaidReady ??= import("mermaid").then(({ default: mermaid }) => {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
    return mermaid;
  });
  return mermaidReady;
}

async function renderMermaidSvg(id: string, code: string): Promise<string> {
  const [mermaid, { default: DOMPurify }] = await Promise.all([loadMermaid(), import("dompurify")]);
  const { svg } = await mermaid.render(id, code);
  return DOMPurify.sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true, html: true },
    ADD_TAGS: ["foreignObject"],
  });
}

// A diagram's source can change many times a second while an agent reply is
// revealed; render only once it has been stable this long.
const MERMAID_SETTLE_MS = 250;

function MermaidDiagram({ code }: { readonly code: string }) {
  const reactId = useId();
  // The render result is tagged with the code it was produced from, so a
  // failure (or an SVG) for an older revision never sticks to new code: the
  // view derives "failed"/"ready" from the CURRENT code only.
  const [result, setResult] = useState<{ code: string; svg: string | null } | null>(null);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void renderMermaidSvg(`mmd${reactId.replace(/[^a-zA-Z0-9]/g, "")}`, code).then(
        (svg) => !cancelled && setResult({ code, svg }),
        () => !cancelled && setResult({ code, svg: null }),
      );
    }, MERMAID_SETTLE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [code, reactId]);

  const current = result?.code === code ? result : null;
  if (current && current.svg === null) {
    return (
      <pre className="my-1 overflow-x-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-2 font-mono text-[11px] leading-5 text-[var(--color-fg-muted)]">
        {code}
      </pre>
    );
  }
  // While the next revision renders, keep showing the last good diagram.
  const svg = current?.svg ?? result?.svg ?? null;
  if (svg === null) {
    return (
      <div className="my-1 rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 text-[11px] text-[var(--color-fg-subtle)]">
        rendering diagram…
      </div>
    );
  }

  return (
    <div
      className="mermaid-diagram my-1 overflow-x-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-2 [&_svg]:mx-auto [&_svg]:h-auto [&_svg]:w-full [&_svg]:max-h-[min(70vh,44rem)] [&_svg]:max-w-full!"
      // Sanitized above (strict mermaid + DOMPurify); see renderMermaidSvg.
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}

function CodeBlock({ code, lang }: { readonly code: string; readonly lang: string | null }) {
  if (lang === "mermaid") return <MermaidDiagram code={code} />;

  return (
    <pre className="my-2 overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] p-3">
      {lang ? (
        <div className="mb-1.5 select-none font-mono text-[0.7rem] uppercase tracking-wide text-[var(--color-fg-subtle)]">
          {lang}
        </div>
      ) : null}
      {/* No language (ASCII diagrams, logs, command output): verbatim, no
          keyword colouring. */}
      <code className="font-mono text-[0.85em] leading-relaxed text-[var(--color-fg)]">{lang ? highlight(code) : code}</code>
    </pre>
  );
}


const components: Components = {
  h1({ children }) {
    return <h1 className="mt-2 mb-1 text-base font-semibold">{children}</h1>;
  },
  h2({ children }) {
    return <h2 className="mt-2 mb-1 text-base font-semibold">{children}</h2>;
  },
  h3({ children }) {
    return <h3 className="mt-2 mb-1 text-sm font-semibold">{children}</h3>;
  },
  h4({ children }) {
    return <h4 className="mt-2 mb-1 text-sm font-semibold">{children}</h4>;
  },
  h5({ children }) {
    return <h5 className="mt-2 mb-1 text-sm font-semibold">{children}</h5>;
  },
  h6({ children }) {
    return <h6 className="mt-2 mb-1 text-sm font-semibold">{children}</h6>;
  },
  p({ children }) {
    return <p className="leading-relaxed">{children}</p>;
  },
  ul({ children }) {
    return <ul className="my-1 list-disc pl-5">{children}</ul>;
  },
  ol({ children }) {
    return <ol className="my-1 list-decimal pl-5">{children}</ol>;
  },
  li({ children, className, ...props }) {
    return (
      <li className={["leading-relaxed", className].filter(Boolean).join(" ")} {...props}>
        {children}
      </li>
    );
  },
  blockquote({ children }) {
    return <blockquote className="my-1 border-l-2 border-[var(--color-border-strong)] pl-3 text-[var(--color-fg-muted)]">{children}</blockquote>;
  },
  a({ children, href }) {
    const safeHref = href ?? "";
    const openLink = (event: MouseEvent<HTMLAnchorElement>) => {
      event.preventDefault();
      if (safeHref) void openExternal(safeHref);
    };

    return (
      <a
        href={safeHref}
        rel="noopener noreferrer"
        onClick={openLink}
        className="text-[var(--color-accent)] underline underline-offset-2 hover:opacity-80"
      >
        {children}
      </a>
    );
  },
  table({ children }) {
    return (
      <div className="my-2 overflow-x-auto">
        <table className="min-w-full border-collapse text-[0.85em]">{children}</table>
      </div>
    );
  },
  th({ children, align }) {
    return (
      <th align={align} className="border-b border-[var(--color-border-strong)] px-2 py-1 text-left font-semibold">
        {children}
      </th>
    );
  },
  td({ children, align }) {
    return (
      <td align={align} className="border-b border-[var(--color-border)] px-2 py-1 align-top">
        {children}
      </td>
    );
  },
  code({ children, className }) {
    const text = String(children);
    if (isCodeBlock(text, className)) return <CodeBlock code={text.replace(/\n$/, "")} lang={codeLanguage(className)} />;

    return <code className="rounded bg-[var(--color-surface-2)] px-1.5 py-0.5 font-mono text-[0.85em] text-[var(--color-fg)]">{children}</code>;
  },
  // CodeBlock renders its own <pre>.
  pre({ children }) {
    return <>{children}</>;
  },
};

export function Markdown({ text }: { readonly text: string }) {
  return (
    <div className="space-y-2 break-words">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {normalizeCrammedTables(text)}
      </ReactMarkdown>
    </div>
  );
}
