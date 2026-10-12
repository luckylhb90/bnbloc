import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { api } from "@/lib/api";
import { bindActingTenant, clearActingTenant, refreshSession, setReauthHandler, setToken } from "@/lib/session";
import {
  SESSION_KEY,
  parseSessionRecord,
  parseSsoFragment,
  recordFromGrant,
  renewDelayMs,
  renewSession,
  type Grant,
  type SessionRecord,
} from "@/lib/sessionGrant";
import { takeDesktopSsoNonce } from "@/lib/desktop";
import { forgetBrowserPush } from "@/lib/webPush";
import { clearQueryCache } from "@/lib/useQuery";
import { forgetLocalAgent } from "@/lib/useLocalAgent";

interface AuthState {
  subject: string | null;
  tenant: string | null;
  role: string | null;
  ready: boolean;
  ssoAvailable: boolean;
  // False when the gateway refuses direct (identity) login — production without
  // OIDC, where sign-in is only through an installed auth-provider.
  directLoginAvailable: boolean;
  ssoStartPath: string | null;
  // tokenEra increments on every session-token swap (login, reauth,
  // tenant switch). Consumers that must react to a NEW token (the desktop
  // shell re-feeds its sidecar adapter/bridge) depend on this instead of the
  // token value itself.
  tokenEra: number;
  login: (idpToken: string) => Promise<void>;
  switchTenant: (tenantId: string) => Promise<void>;
  logout: () => void;
  reauth: () => Promise<void>;
}

const AuthCtx = createContext<AuthState>(null!);
export const useAuth = () => useContext(AuthCtx);

// sessionStore: the session persists in localStorage everywhere — reopening
// the app or the browser lands you signed-in. The 1h session bearer is renewed
// from the stored ~30d refresh token; Sign Out clears both.
const sessionStore = (): Storage | null => {
  try {
    if (typeof window === "undefined") return null;
    return window.localStorage;
  } catch {
    return null;
  }
};

function storedSession(): SessionRecord | null {
  return parseSessionRecord(sessionStore()?.getItem(SESSION_KEY));
}

function persistSession(record: SessionRecord) {
  sessionStore()?.setItem(SESSION_KEY, JSON.stringify(record));
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [subject, setSubject] = useState<string | null>(null);
  const [tenant, setTenant] = useState<string | null>(null);
  const [role, setRole] = useState<string | null>(null);
  const [ready, setReady] = useState(false);
  const [ssoAvailable, setSsoAvailable] = useState(false);
  const [directLoginAvailable, setDirectLoginAvailable] = useState(true);
  const [ssoStartPath, setSsoStartPath] = useState<string | null>(null);
  const [tokenEra, setTokenEra] = useState(0);
  // apply installs a session record: bearer, state, persistence, and a
  // tokenEra bump so the desktop shell re-feeds its sidecars/bridge.
  const apply = useCallback((record: SessionRecord) => {
    setToken(record.token);
    bindActingTenant(record.subject);
    setTokenEra((n) => n + 1);
    setSubject(record.subject);
    setTenant(record.tenant);
    setRole(record.role);
    persistSession(record);
  }, []);

  useEffect(() => {
    api
      .ssoConfig()
      .then((c) => {
        setSsoAvailable(!!c.authProviderAvailable);
        setDirectLoginAvailable(c.directLoginAvailable !== false);
        setSsoStartPath(c.authProviderStartPath ?? null);
      })
      .catch(() => {});
    // Restore an SSO-callback grant from the URL hash, else the persisted
    // session. Every sign-in path lands through adopt() with its refresh token.
    const adopt = (g: Grant) => {
      apply(recordFromGrant(g));
      setReady(true);
    };
    const fragment = parseSsoFragment(window.location.hash);
    // Desktop SSO hand-off: the bnbloc://sso deep link delivers only a
    // one-time code. Redeem it with the nonce this app stored when IT started
    // the flow; a code with no pending nonce was not initiated here (login
    // CSRF) and is dropped.
    if (fragment.ssoCode) {
      window.history.replaceState(null, "", "/");
      const nonce = takeDesktopSsoNonce();
      if (nonce) {
        api
          .ssoExchange(fragment.ssoCode, nonce)
          .then(adopt)
          .catch(() => setReady(true));
        return;
      }
    }
    if (fragment.grant) {
      window.history.replaceState(null, "", "/");
      adopt(fragment.grant);
      return;
    }
    const saved = storedSession();
    if (saved) {
      setToken(saved.token);
      bindActingTenant(saved.subject);
      setSubject(saved.subject);
      setTenant(saved.tenant);
      setRole(saved.role);
    }
    setReady(true);
  }, []);

  // Mirrors of the session scope for the re-auth handler, which is installed
  // once and must read the CURRENT subject, not a stale closure.
  const subjectRef = useRef(subject);
  subjectRef.current = subject;
  const directLoginRef = useRef(directLoginAvailable);
  directLoginRef.current = directLoginAvailable;

  // establish mints a grant (login or refresh) and installs it. The gateway
  // mints it in the tenant the member chose: a tenant switch persists on the
  // sign-in's refresh family, so no client-side re-switch is needed.
  const establish = useCallback(
    async (mint: () => Promise<Grant>) => {
      const record = recordFromGrant(await mint());
      apply(record);
      return record;
    },
    [apply],
  );

  const login = useCallback(
    async (idpToken: string) => {
      await establish(() => api.login(idpToken));
    },
    [establish],
  );

  const switchTenant = useCallback(async (tenantId: string) => {
    const { sessionToken, subject, tenant, role } = await api.switchTenant(tenantId);
    setToken(sessionToken);
    setSubject(subject);
    setTenant(tenant ?? null);
    setRole(role ?? null);
    // switch-tenant mints no refresh token: the sign-in's stays, and its
    // family now refreshes into the switched tenant (server-side).
    persistSession({
      token: sessionToken,
      refreshToken: storedSession()?.refreshToken ?? null,
      subject,
      tenant: tenant ?? null,
      role: role ?? null,
    });
    // Rooms/agents/RBAC are all scoped to the token's tenant; reload so every
    // view re-fetches under the new scope instead of showing stale data.
    clearQueryCache();
    window.location.reload();
  }, []);

  const logout = useCallback(() => {
    const refreshToken = storedSession()?.refreshToken;
    clearActingTenant();
    // The next member never reads this one's cached answers.
    clearQueryCache();
    forgetLocalAgent();
    setToken(null);
    setSubject(null);
    setTenant(null);
    setRole(null);
    sessionStore()?.removeItem(SESSION_KEY);
    // Revoke the sign-in server-side so the refresh token cannot renew a
    // session again. Best-effort: the local sign-out above never waits on it.
    if (refreshToken) void api.logout(refreshToken).catch(() => {});
    // This browser stops receiving the signed-out member's push notifications.
    void forgetBrowserPush().catch(() => {});
  }, []);

  // The ONE silent re-auth path. Every 401 recovery — api requests, uploads,
  // downloads, the SSE loop, an explicit join, the proactive renewal below —
  // funnels through session.refreshSession(), which coalesces concurrent
  // callers onto this handler. renewSession decides: refresh token → refresh;
  // none → direct re-login only where the gateway offers it; else sign out.
  // The gateway mints the new grant in the tenant the member chose. The
  // subject comes from refs/storage, not render closures: the handler can run
  // during the first render after a reload, before state is populated.
  useEffect(() => {
    setReauthHandler(() => {
      const stored = storedSession();
      return renewSession({
        stored,
        subject: subjectRef.current,
        directLoginAvailable: directLoginRef.current,
        refresh: api.refresh,
        login: api.login,
        establish,
        logout,
      });
    });
    return () => setReauthHandler(null);
  }, [establish, logout]);

  // Proactive renewal: one timer renews the session shortly before its bearer
  // expires, so long-lived SSE streams and uploads never hit a dead token.
  // Re-armed on every token swap (tokenEra) and cleared on sign-out/unmount.
  // Only with a refresh token: without one a renewal would end the session.
  useEffect(() => {
    if (!subject) return;
    const stored = storedSession();
    if (!stored?.refreshToken) return;
    const delay = renewDelayMs(stored.token, Date.now());
    if (delay === null) return;
    const timer = window.setTimeout(() => void refreshSession(), delay);
    return () => window.clearTimeout(timer);
  }, [subject, tokenEra]);

  // reauth is the context-facing form of the same refresh; it throws when the
  // session could not be renewed so callers (join) surface the failure.
  const reauth = useCallback(async () => {
    if (!(await refreshSession())) throw new Error("Could not refresh your session");
  }, []);

  const value = useMemo(
    () => ({ subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra, login, switchTenant, logout, reauth }),
    [subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra, login, switchTenant, logout, reauth],
  );
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}
