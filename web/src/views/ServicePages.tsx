import { useEffect, useState } from "react";
import { ArrowSquareOutIcon } from "@phosphor-icons/react";
import { componentPagesApi, openComponentPage } from "@/lib/componentPagesApi";
import { errorText } from "@/lib/errors";
import { pageSource } from "@/lib/pageSource";
import type { ComponentPageInfo } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";

// ServicePages lists the pages of the organization's services the member
// may open (docs/architecture/service-components.md): each opens in a new
// tab as the member. Nothing shows when there are none.
export function ServicePages() {
  const [pages, setPages] = useState<readonly ComponentPageInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    componentPagesApi
      .list()
      .then((r) => live && setPages(r.pages ?? []))
      .catch(() => live && setPages([]));
    return () => {
      live = false;
    };
  }, []);
  if (pages.length === 0) return null;
  const open = async (p: ComponentPageInfo) => {
    setError(null);
    try {
      await openComponentPage(p.component, p.name);
    } catch (e) {
      setError(errorText(e, `Could not open ${p.label}`));
    }
  };
  return (
    <section className="flex flex-col gap-2" data-service-pages>
      <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">Your organization's services</span>
      {pages.map((p) => (
        <Card key={`${p.component}/${p.name}`} className="flex items-center justify-between gap-3 px-4 py-3">
          <div className="min-w-0">
            <p className="text-sm font-medium">{p.label}</p>
            <p className="text-xs text-[var(--color-fg-muted)]">
              {pageSource(p)}
              {p.audience === "admin" ? " · for admins" : ""}
            </p>
          </div>
          <Button size="sm" onClick={() => void open(p)}>
            <ArrowSquareOutIcon size={14} aria-hidden="true" /> Open
          </Button>
        </Card>
      ))}
      {error && (
        <p role="alert" className="text-xs text-[var(--color-danger)]">
          {error}
        </p>
      )}
    </section>
  );
}
