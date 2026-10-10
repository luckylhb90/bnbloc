import { lazy, Suspense, type ComponentProps } from "react";

// The workbench (tabs, viewers, version tools) and the deliverables view load
// on demand: a room's conversation needs neither until a file opens or the
// Files panel is selected. File cards in the feed stay in the room bundle.
const PreviewWorkbench = lazy(() => import("./PreviewWorkbench").then((m) => ({ default: m.PreviewWorkbench })));
const DeliverablesView = lazy(() => import("./DeliverablesView").then((m) => ({ default: m.DeliverablesView })));

const fallback = <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading…</p>;

export function LazyPreviewWorkbench(props: ComponentProps<typeof PreviewWorkbench>) {
  return (
    <Suspense fallback={fallback}>
      <PreviewWorkbench {...props} />
    </Suspense>
  );
}

export function LazyDeliverablesView(props: ComponentProps<typeof DeliverablesView>) {
  return (
    <Suspense fallback={fallback}>
      <DeliverablesView {...props} />
    </Suspense>
  );
}
