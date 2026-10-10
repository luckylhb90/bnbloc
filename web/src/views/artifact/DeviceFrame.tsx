import type { ReactNode } from "react";

// DeviceFrame lays a running preview out at a device width: "fill" takes the
// whole tab; a pixel width centers a bordered frame of that width (scrolling
// sideways when the tab is narrower). Shared by the HTML runner and the web
// app (site) viewer so their width presets behave the same.
export function DeviceFrame({ width, children }: { readonly width: string; readonly children: ReactNode }) {
  const fixed = width !== "fill";
  return (
    <div className={fixed ? "h-full overflow-auto bg-[var(--color-bg)] p-3" : "h-full"}>
      <div
        className={fixed ? "mx-auto h-full overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] shadow-[var(--shadow-pop)]" : "h-full"}
        style={fixed ? { width: `${width}px`, maxWidth: "none" } : undefined}
      >
        {children}
      </div>
    </div>
  );
}
