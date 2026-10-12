import { lazy, StrictMode, Suspense, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import { AuthProvider } from "@/lib/auth";
import { ToastProvider } from "@/ui/Toast";
import { ResubmissionToasts } from "@/views/ResubmissionToasts";
import { ErrorBoundary } from "@/ui/ErrorBoundary";
import { App } from "@/App";
import { seedPreviewSession } from "@/lib/desktop";
import { inDesktopShell } from "@/lib/config";
import { registerAppWorkerAfterLoad } from "@/lib/appWorker";
import { trackVisualViewport } from "@/lib/viewport";
import { shareTokenFromHash } from "@/lib/hashRoute";

// The public share page renders before (and without) sign-in: it is outside
// AuthProvider, so it never reads, renews or sends a session, and it calls
// only the public share routes. Its code loads only for share links.
const PublicSharePage = lazy(() =>
  import("@/views/share/PublicSharePage").then((m) => ({ default: m.PublicSharePage })),
);

// Root picks the public share page for a `#/share/<token>` hash, else the
// signed-in app; a hash change (a share link opened in this tab) switches.
function Root() {
  const [shareToken, setShareToken] = useState(() => shareTokenFromHash(window.location.hash));
  useEffect(() => {
    const onHash = () => setShareToken(shareTokenFromHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  if (shareToken) {
    return (
      <Suspense fallback={null}>
        <PublicSharePage key={shareToken} token={shareToken} />
      </Suspense>
    );
  }
  return (
    <AuthProvider>
      <App />
    </AuthProvider>
  );
}

// A torn-out preview window adopts its parent's session before anything
// renders, so useAuth's boot read finds it and the view skips Login.
seedPreviewSession();

// handCodeToDesktopApp: an SSO callback landing in a plain browser for a flow
// the desktop app initiated (it opens SSO in the system browser). For such a
// flow the gateway returns only a short-lived one-time code bound to the app's
// nonce — never a session token — and this page forwards it to the app through
// the bnbloc:// deep link; the app redeems it over HTTPS. A browser-initiated
// login (#sessionToken) stays in the browser and is never handed to the app.
if (!inDesktopShell()) {
  const code = new URLSearchParams(window.location.hash.slice(1)).get("ssoCode");
  if (code) {
    window.history.replaceState(null, "", "/");
    const link = document.createElement("a");
    link.href = "bnbloc://sso?code=" + encodeURIComponent(code);
    link.click();
  }
}

// cliSignedIn: the SSO callback of a sign-in the bnbloc CLI started
// (`bnbloc login`). The gateway parked the session for the CLI to claim with
// its own nonce; this page carries no token or code, so it adopts nothing and
// only tells the user the terminal is signed in.
const cliSignedIn = !inDesktopShell() && new URLSearchParams(window.location.hash.slice(1)).get("cliSignedIn") === "1";
if (cliSignedIn) window.history.replaceState(null, "", "/");

// The app's service worker (offline shell + browser push; never in the
// desktop shell) and the on-screen keyboard sizing for touch devices (R14).
registerAppWorkerAfterLoad();
trackVisualViewport();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {cliSignedIn ? (
      <main className="flex min-h-screen items-center justify-center p-6 text-center text-[var(--color-fg)]">
        <p className="text-base">Signed in to the bnbloc CLI — you can close this tab.</p>
      </main>
    ) : (
      <ErrorBoundary>
        <ToastProvider>
          <Root />
          <ResubmissionToasts />
        </ToastProvider>
      </ErrorBoundary>
    )}
  </StrictMode>,
);
