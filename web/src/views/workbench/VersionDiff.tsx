import { useMemo } from "react";
import { diffLines } from "@/lib/diff";
import { cn } from "@/lib/cn";

// Line diff of two text versions (any two: the version picker chooses both).
export function VersionDiff({ prev, curr }: { readonly prev: string; readonly curr: string }) {
  const rows = useMemo(() => diffLines(prev, curr), [prev, curr]);
  if (!rows.some((r) => r.type !== "same")) {
    return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">No changes.</p>;
  }
  return (
    <div className="h-full overflow-auto py-2 font-mono text-[12px] leading-5">
      {rows.map((r, i) => (
        <div
          key={i}
          className={cn(
            "flex gap-2 px-3",
            r.type === "add" && "bg-[color-mix(in_srgb,var(--color-accent)_12%,transparent)]",
            r.type === "del" && "bg-[color-mix(in_srgb,var(--color-danger,#b91c1c)_12%,transparent)] opacity-70",
          )}
        >
          <span className="w-4 shrink-0 select-none text-[var(--color-fg-subtle)]">
            {r.type === "add" ? "+" : r.type === "del" ? "−" : " "}
          </span>
          <span className="whitespace-pre-wrap break-all">{r.text || " "}</span>
        </div>
      ))}
    </div>
  );
}
