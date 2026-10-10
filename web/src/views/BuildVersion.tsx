import { useEffect, useState } from "react";
import { buildLabel, buildTitle } from "@/lib/buildInfo";
import { desktop } from "@/lib/desktop";
import { cn } from "@/lib/cn";

// BuildVersion is the quiet version stamp (login page, sidebar). Hovering shows
// the commit and, in the desktop shell, the app version when it differs.
export function BuildVersion({ className }: { readonly className?: string }) {
  const [desktopVersion, setDesktopVersion] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    void desktop.appInfo().then((info) => {
      if (live && info?.inShell) setDesktopVersion(info.version);
    }).catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);
  return (
    <span className={cn("tabular select-text text-[11px] text-[var(--color-fg-subtle)]", className)} title={buildTitle(desktopVersion)}>
      {buildLabel()}
    </span>
  );
}
