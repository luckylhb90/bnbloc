import { useState } from "react";
import { clearGatewayOverride, defaultGatewayBase, gatewayBase, setGatewayOverride } from "@/lib/config";
import { Button } from "@/ui/Button";
import { Field, Input } from "@/ui/Input";

// GatewaySwitcher points this client at a gateway. Its only presets come from
// configuration — the build's default gateway, if the build has one — never
// from host names baked into the source. Applying a gateway reloads the app so
// every boot-time read (sign-in options, session) runs against it.
export function GatewaySwitcher({ firstRun = false }: { readonly firstRun?: boolean }) {
  const active = gatewayBase();
  const builtIn = defaultGatewayBase();
  const [custom, setCustom] = useState(active);
  const [error, setError] = useState<string | null>(null);

  const apply = (url: string) => {
    const next = url.trim();
    try {
      const parsed = new URL(next);
      if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("protocol");
    } catch {
      setError("Enter a full gateway URL, for example https://bnbloc.example.com");
      return;
    }
    setGatewayOverride(next);
    window.location.reload();
  };

  const reset = () => {
    clearGatewayOverride();
    window.location.reload();
  };

  return (
    <section
      aria-label={firstRun ? "Connect to your gateway" : "Gateway"}
      className="absolute inset-x-4 bottom-4 z-20 mx-auto max-w-md rounded-[var(--radius-lg)] border border-[var(--color-border)]/80 bg-[var(--color-surface)]/95 p-3 text-xs text-[var(--color-fg-muted)] shadow-[var(--shadow-diffuse)] backdrop-blur sm:right-4 sm:left-auto sm:mx-0"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="font-medium text-[var(--color-fg)]">
            {firstRun ? "Connect to your bnbloc gateway" : "Gateway"}
          </div>
          <div className="mt-1 text-[var(--color-fg-subtle)]">
            {firstRun
              ? "This app was not set up with a gateway. Enter the address your admin gave you to sign in."
              : <span className="block max-w-[32ch] truncate">Active: {active || "none"}</span>}
          </div>
        </div>
        {!firstRun && builtIn && active !== builtIn && (
          <Button variant="ghost" size="sm" onClick={reset}>
            Use default
          </Button>
        )}
      </div>

      <div className="mt-3">
        <Field label="Gateway URL" error={error ?? undefined}>
          <div className="flex gap-2">
            <Input
              autoFocus={firstRun}
              value={custom}
              placeholder="https://bnbloc.example.com"
              onChange={(event) => {
                setCustom(event.target.value);
                setError(null);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  apply(custom);
                }
              }}
            />
            <Button variant="secondary" size="md" onClick={() => apply(custom)} disabled={!custom.trim()}>
              {firstRun ? "Connect" : "Apply"}
            </Button>
          </div>
        </Field>
      </div>
    </section>
  );
}
