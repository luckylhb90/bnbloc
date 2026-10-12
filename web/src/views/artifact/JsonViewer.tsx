import { useMemo, useState } from "react";
import { CaretDownIcon, CaretRightIcon } from "@phosphor-icons/react";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { errorText } from "@/lib/errors";

// JsonViewer: a collapsible tree that stays responsive on large documents —
// children render only when their node is open, at most PAGE per node until
// "show more", and "expand all" is offered only below EXPAND_ALL_MAX nodes.
// Values render as text nodes (no markup). Invalid JSON shows the parse error
// and the raw text.

const PAGE = 200;
const EXPAND_ALL_MAX = 5000;
const RAW_PREVIEW_CHARS = 200_000;

function countNodes(value: unknown, limit: number): number {
  let count = 0;
  const stack: unknown[] = [value];
  while (stack.length > 0 && count <= limit) {
    const v = stack.pop();
    count += 1;
    if (v && typeof v === "object") for (const child of Object.values(v as Record<string, unknown>)) stack.push(child);
  }
  return count;
}

function Scalar({ value }: { readonly value: unknown }) {
  if (typeof value === "string") return <span className="break-all text-[var(--color-accent)]">{JSON.stringify(value)}</span>;
  if (typeof value === "number") return <span className="text-[var(--color-warning)]">{String(value)}</span>;
  return <span className="text-[var(--color-warning)]">{String(value)}</span>;
}

function JsonNode({
  name,
  value,
  depth,
  openDepth,
}: {
  readonly name: string | null;
  readonly value: unknown;
  readonly depth: number;
  readonly openDepth: number;
}) {
  const [open, setOpen] = useState(depth < openDepth);
  const [shown, setShown] = useState(PAGE);
  const label = name !== null && <span className="text-[var(--color-fg)]">{name}: </span>;
  if (!value || typeof value !== "object") {
    return (
      <div className="pl-5">
        {label}
        <Scalar value={value} />
      </div>
    );
  }
  const isArray = Array.isArray(value);
  const entries = isArray ? (value as unknown[]).map((v, i) => [String(i), v] as const) : Object.entries(value as Record<string, unknown>);
  const summary = isArray ? `[${entries.length}]` : `{${entries.length}}`;
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex items-center gap-1 rounded text-left hover:bg-[var(--color-surface-2)]"
      >
        {open ? <CaretDownIcon size={12} className="shrink-0" /> : <CaretRightIcon size={12} className="shrink-0" />}
        {label}
        <span className="text-[var(--color-fg-subtle)]">{summary}</span>
      </button>
      {open && (
        <div className="ml-[7px] border-l border-[var(--color-border)] pl-2">
          {entries.slice(0, shown).map(([key, child]) => (
            <JsonNode key={key} name={isArray ? key : JSON.stringify(key)} value={child} depth={depth + 1} openDepth={openDepth} />
          ))}
          {entries.length > shown && (
            <button
              type="button"
              onClick={() => setShown((n) => n + PAGE)}
              className="pl-5 text-[var(--color-accent)] hover:underline"
            >
              Show {Math.min(PAGE, entries.length - shown)} more of {entries.length - shown}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export default function JsonViewer({ content, toolbar }: ArtifactViewerProps) {
  const parsed = useMemo(() => {
    try {
      return { ok: true as const, value: JSON.parse(content) as unknown };
    } catch (e) {
      return { ok: false as const, error: errorText(e, "Invalid JSON") };
    }
  }, [content]);
  const small = useMemo(() => parsed.ok && countNodes(parsed.value, EXPAND_ALL_MAX) <= EXPAND_ALL_MAX, [parsed]);
  // Remount the tree with a new open depth for expand/collapse all.
  const [tree, setTree] = useState({ gen: 0, openDepth: 1 });
  useViewerToolbar(
    toolbar,
    [
      { id: "expand", kind: "button", icon: "expand-all", label: "Expand all", disabled: !small },
      { id: "collapse", kind: "button", icon: "collapse-all", label: "Collapse all" },
    ],
    (id) => {
      if (id === "expand") setTree((t) => ({ gen: t.gen + 1, openDepth: Infinity }));
      else if (id === "collapse") setTree((t) => ({ gen: t.gen + 1, openDepth: 1 }));
    },
  );
  if (!parsed.ok) {
    return (
      <div className="h-full overflow-auto p-3 font-mono text-[12px]">
        <p className="mb-2 text-[var(--color-danger,#f87171)]">Invalid JSON: {parsed.error}</p>
        <pre className="whitespace-pre-wrap break-all text-[var(--color-fg-muted)]">{content.slice(0, RAW_PREVIEW_CHARS)}</pre>
      </div>
    );
  }
  return (
    <div className="h-full overflow-auto p-3 font-mono text-[12px] leading-5 text-[var(--color-fg-muted)]">
      <JsonNode key={tree.gen} name={null} value={parsed.value} depth={0} openDepth={tree.openDepth} />
    </div>
  );
}
