import { useEffect, useState } from "react";
import { ArrowRightIcon, WaveformIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { accountKind, keyOf } from "@/lib/accounts";
import { TestBadge } from "@/views/accounts/AccountsDialog";
import { Button } from "@/ui/Button";
import { Input } from "@/ui/Input";
import { Mascot } from "@/ui/Mascot";
import { useToast } from "@/ui/Toast";
import { gatewayBase, inDesktopShell, needsGatewaySetup } from "@/lib/config";
import { beginDesktopSso, openExternal } from "@/lib/desktop";
import { beginDeepLinkSignIn } from "@/lib/loginReturn";
import { cn } from "@/lib/cn";
import { GatewaySwitcher } from "@/views/GatewaySwitcher";
import { LoginBackground } from "@/views/LoginBackground";
import { BuildVersion } from "@/views/BuildVersion";

// Rendered twice by design: the lg+ brand panel, and the sign-in card below
// lg where that panel is hidden.
function Pitch({ compact }: { compact?: boolean }) {
  return (
    <div className={compact ? undefined : "max-w-md"}>
      <h1
        className={cn(
          "font-semibold leading-tight tracking-tighter text-[var(--color-fg)]",
          compact ? "text-3xl" : "text-4xl",
        )}
      >
        Where people and agents build in tune.
      </h1>
      <p
        className={cn(
          "leading-relaxed text-[var(--color-fg-muted)]",
          compact ? "mt-3 text-sm" : "mt-4 max-w-[48ch]",
        )}
      >
        One shared room for multi-user, multi-agent collaboration — real-time
        presence, cross-user agent borrowing, and live activity, wired to your
        own local agent core.
      </p>
    </div>
  );
}

export function Login() {
  const { login, ssoAvailable, directLoginAvailable, ssoStartPath, multiAccount, accounts, switchAccount } = useAuth();
  const [who, setWho] = useState("");
  const [busy, setBusy] = useState(false);
  // A desktop build without a default gateway has nothing to sign in to: the
  // gateway prompt opens by itself (first run). Otherwise it stays a
  // Cmd/Ctrl+Shift+G developer affordance.
  const firstRun = needsGatewaySetup();
  const [showGatewaySwitcher, setShowGatewaySwitcher] = useState(firstRun);
  const toast = useToast();

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === "g") {
        event.preventDefault();
        setShowGatewaySwitcher((visible) => firstRun || !visible);
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  const submit = async (e: { preventDefault: () => void }) => {
    e.preventDefault();
    if (!who.trim() || busy) return;
    if (firstRun) {
      toast.info("Connect to your gateway first");
      return;
    }
    setBusy(true);
    try {
      await login(who.trim());
    } catch {
      toast.error("Sign in failed — check your identity and try again");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="relative grid min-h-[100dvh] overflow-hidden bg-[var(--color-bg)] lg:grid-cols-[1.1fr_1fr]">
      <LoginBackground />
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(60rem 40rem at 20% -10%, var(--color-accent-soft), transparent 60%)",
        }}
      />
      {/* Brand panel — asymmetric, editorial. */}
      <div className="relative hidden min-w-0 overflow-hidden border-r border-[var(--color-border)]/50 bg-[var(--color-surface)]/30 p-12 shadow-[inset_-1px_0_0_hsl(240_6%_100%_/_0.03)] lg:flex flex-col justify-between">
        <div className="relative flex items-center gap-2 text-[var(--color-fg)]">
          <WaveformIcon size={22} weight="fill" className="text-[var(--color-accent)]" />
          <span className="text-lg font-semibold tracking-tight">bnbloc</span>
        </div>
        <div className="relative">
          <Pitch />
        </div>
        <div className="relative flex items-center gap-6 text-xs text-[var(--color-fg-subtle)] tabular">
          <span>Self-hosted</span>
          <span>OIDC / SSO</span>
          <span>Pluggable core</span>
        </div>
      </div>

      {/* Sign-in panel. */}
      <div className="relative flex min-w-0 items-center justify-center p-6">
        <BuildVersion className="absolute bottom-4 right-6" />
        <div className="w-full max-w-sm animate-in rounded-[var(--radius-xl)] border border-[var(--color-border)]/70 bg-[var(--color-bg)]/78 p-6 shadow-[var(--shadow-diffuse),inset_0_1px_0_hsl(240_6%_100%_/_0.04)] sm:p-8 lg:rounded-none lg:border-0 lg:bg-transparent lg:p-0 lg:shadow-none">
          <div className="mb-8 flex items-center gap-2 lg:hidden">
            <WaveformIcon size={20} weight="fill" className="text-[var(--color-accent)]" />
            <span className="text-lg font-semibold tracking-tight">bnbloc</span>
          </div>
          <div className="mb-8 lg:hidden">
            <Pitch compact />
          </div>
          <h2 className="text-2xl font-semibold tracking-tight">Welcome back</h2>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Sign in to enter your workspace.
          </p>
          {multiAccount && accounts.length > 0 && (
            <div className="mt-6 flex flex-col gap-2">
              <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Accounts on this browser</span>
              {accounts.map((a) => (
                <Button
                  key={keyOf(a)}
                  variant="outline"
                  className="justify-between"
                  onClick={() => void switchAccount(keyOf(a)).catch(() => toast.error("That account can no longer sign in"))}
                >
                  <span className="truncate">Continue as {a.record.subject}</span>
                  {accountKind(a.record) === "test" && <TestBadge />}
                </Button>
              ))}
            </div>
          )}

          {directLoginAvailable && (
          <form onSubmit={submit} className="mt-8 flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label
                htmlFor="who"
                className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]"
              >
                Identity
              </label>
              <Input
                id="who"
                autoFocus
                placeholder="you@company.com"
                value={who}
                onChange={(e) => setWho(e.target.value)}
              />
            </div>
            <Button
              type="submit"
              variant="primary"
              size="lg"
              disabled={busy || !who.trim()}
              className="w-full justify-center"
            >
              {busy ? "Signing in…" : "Continue"}
              {!busy && <ArrowRightIcon size={16} />}
            </Button>
          </form>
          )}

          {!directLoginAvailable && !ssoAvailable && (
            <p role="status" className="mt-8 text-sm text-[var(--color-fg-muted)]">
              Sign-in isn't configured on this gateway yet. Ask your administrator to set up single sign-on.
            </p>
          )}

          {ssoAvailable && (
            <>
              {directLoginAvailable ? (
                <div className="my-6 flex items-center gap-3 text-xs text-[var(--color-fg-subtle)]">
                  <span className="h-px flex-1 bg-[var(--color-border)]" />
                  or
                  <span className="h-px flex-1 bg-[var(--color-border)]" />
                </div>
              ) : (
                <div className="mt-8" />
              )}
              <Button
                variant="outline"
                size="lg"
                className="w-full justify-center"
                onClick={() => {
                  const base = gatewayBase();
                  const cb = (base || window.location.origin) + "/sso-callback";
                  if (!ssoStartPath) return;
                  // A card or a notification opened a room link without a
                  // session: sign-in brings the person back to it.
                  beginDeepLinkSignIn(window.location.hash, Date.now());
                  if (inDesktopShell()) {
                    // The callback carries this app's one-time nonce; the
                    // gateway answers with a nonce-bound code, never a token.
                    const desktopCb = cb + "?desktop_nonce=" + encodeURIComponent(beginDesktopSso());
                    void openExternal(base + ssoStartPath + "?redirect_uri=" + encodeURIComponent(desktopCb));
                  } else {
                    window.location.href = base + ssoStartPath + "?redirect_uri=" + encodeURIComponent(cb);
                  }
                }}
              >
                Continue with SSO
              </Button>
            </>
          )}
        </div>
      </div>
      {showGatewaySwitcher && <GatewaySwitcher firstRun={firstRun} />}
      <Mascot floating size={104} label="Drag your workspace companion, or give it a boop" />
    </div>
  );
}
