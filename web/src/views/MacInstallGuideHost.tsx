import { lazy, Suspense, useSyncExternalStore } from "react";
import { closeMacInstallGuide, macInstallGuideOpen, subscribeMacInstallGuide } from "@/lib/macInstall";

const MacInstallGuideDialog = lazy(() =>
  import("@/views/MacInstallGuide").then((m) => ({ default: m.MacInstallGuideDialog })),
);

// MacInstallGuideHost renders the one macOS install guide (mounted once in
// App): any macOS installer download, and the Release Hub's "Trouble opening
// the app?", opens it. Its code loads only when it first opens.
export function MacInstallGuideHost() {
  const open = useSyncExternalStore(subscribeMacInstallGuide, macInstallGuideOpen);
  if (!open) return null;
  return (
    <Suspense fallback={null}>
      <MacInstallGuideDialog open onClose={closeMacInstallGuide} />
    </Suspense>
  );
}
