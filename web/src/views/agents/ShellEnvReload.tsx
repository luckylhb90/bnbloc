import { useState } from "react";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import { shellEnvReloadText } from "@/lib/sandboxStatus";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { useToast } from "@/ui/Toast";

// ShellEnvReload reads the member's login-shell environment again; the
// bridge, the sidecars and the agents they run restart with it (desktop
// shell only).
export function ShellEnvReload() {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const reload = async () => {
    setBusy(true);
    try {
      const r = await desktop.reloadShellEnv();
      if (r) {
        const { ok, text } = shellEnvReloadText(r);
        if (ok) toast.success(text);
        else toast.error(text);
      }
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card className="flex flex-wrap items-center justify-between gap-3 p-4">
      <p className="min-w-0 flex-1 text-xs text-[var(--color-fg-muted)]">
        Your agents get the variables your login shell exports (such as in ~/.zshrc or ~/.zprofile), read when the app starts. After exporting a new one, reload it
        here: the bridge and your agents restart.
      </p>
      <Button size="sm" variant="ghost" disabled={busy} onClick={() => void reload()}>
        {busy ? "Reloading…" : "Reload shell environment"}
      </Button>
    </Card>
  );
}
