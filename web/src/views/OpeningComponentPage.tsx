import { useEffect, useState } from "react";
import { componentPagesApi } from "@/lib/componentPagesApi";
import { errorText } from "@/lib/errors";
import type { ComponentPageRoute } from "@/lib/previewRoute";

// OpeningComponentPage is the tab openComponentPage opens (#/open-page/..):
// it asks for the page's address and replaces itself with it, so the page
// lives in this very tab (and keeps whatever its host gave it). The tab
// lets go of the app window that opened it first: the page is the
// component's, not the app's.
export function OpeningComponentPage({ route }: { route: ComponentPageRoute }) {
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    document.title = "Opening… · bnbloc";
    window.opener = null;
    let live = true;
    componentPagesApi
      .open(route.component, route.page, route.query, route.roomId)
      .then(({ url }) => {
        if (live) window.location.replace(url);
      })
      .catch((e: unknown) => {
        if (live) setError(errorText(e, "Could not open the page"));
      });
    return () => {
      live = false;
    };
  }, [route.component, route.page, route.query, route.roomId]);

  return (
    <div className="grid min-h-[100dvh] place-items-center bg-[var(--color-bg)] p-6">
      {error ? (
        <p role="alert" className="max-w-md text-center text-sm text-[var(--color-fg-muted)]">
          {error}
        </p>
      ) : (
        <div
          role="status"
          aria-label="Opening"
          className="size-6 animate-spin rounded-full border-2 border-[var(--color-border-strong)] border-t-[var(--color-accent)]"
        />
      )}
    </div>
  );
}
