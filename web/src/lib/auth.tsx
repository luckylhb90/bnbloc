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
import { gatewayBase, inDesktopShell } from "@/lib/config";
import {
  accountKind,
  accountsOn,
  keyOf,
  park,
  readAccounts,
  sessionSubjectChanged,
  unpark,
  writeAccounts,
  type Account,
  type AccountKind,
} from "@/lib/accounts";
import { bindActingTenant, clearActingTenant, refreshSession, setReauthHandler, setToken } from "@/lib/session";
import {
  SESSION_KEY,
  credentialMint,
  errorStatus,
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
import { StorageKey, readStored, writeStored } from "@/lib/storageKeys";

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
  // The accounts of this browser besides the signed-in one, on this gateway
  // (docs/prd/2026-10-22-multi-account.md). multiAccount is false where
  // switching is not supported yet (the desktop shell's bridge is bound to
  // one account).
  multiAccount: boolean;
  // Whether the signed-in account is a person who signed in or a test account.
  kind: AccountKind;
  accounts: readonly Account[];
  switchAccount: (key: string) => Promise<void>;
  // Parks the signed-in account and goes to sign in as another one.
  addAccount: () => void;
  // Adds a test account by its API token; activate makes it the signed-in one.
  addTestAccount: (apiToken: string, activate: boolean) => Promise<void>;
  removeAccount: (key: string) => Promise<void>;
}

const AuthCtx = createContext<AuthState>(null!);
export const useAuth = () => useContext(AuthCtx);

// sessionStore: the session persists in localStorage everywhere — reopening
// the app or the browser lands you signed-in. The 1h session bearer is renewed
// from the stored ~30d refresh token; Sign Out clears both.

function storedSession(): SessionRecord | null {
  return parseSessionRecord(readStored(SESSION_KEY) || null);
}

function persistSession(record: SessionRecord) {
  writeStored(SESSION_KEY, JSON.stringify(record));
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
  // The accounts parked in this browser (every gateway; the context shows this
  // gateway's).
  const [parked, setParked] = useState<Account[]>(() => readAccounts());
  const saveParked = useCallback((list: Account[]) => {
    writeAccounts(list);
    setParked(list);
  }, []);
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
    // The signed-in account is never also a parked one.
    saveParked(unpark(readAccounts(), gatewayBase(), record.subject));
  }, [saveParked]);

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
      apiToken: storedSession()?.apiToken ?? null,
      subject,
      tenant: tenant ?? null,
      role: role ?? null,
    });
    // Rooms/agents/RBAC are all scoped to the token's tenant; reload so every
    // view re-fetches under the new scope instead of showing stale data.
    clearQueryCache();
    window.location.reload();
  }, []);

  // signOutLocally ends this window's session without touching the gateway:
  // the next account never reads this one's cached answers, and this browser
  // stops receiving its push notifications.
  const signOutLocally = useCallback(() => {
    clearActingTenant();
    // The next member never reads this one's cached answers.
    clearQueryCache();
    forgetLocalAgent();
    setToken(null);
    setSubject(null);
    setTenant(null);
    setRole(null);
    writeStored(SESSION_KEY, "");
    void forgetBrowserPush().catch(() => {});
  }, []);

  const logout = useCallback(() => {
    const refreshToken = storedSession()?.refreshToken;
    signOutLocally();
    // Revoke the sign-in server-side so the refresh token cannot renew a
    // session again. Best-effort: the local sign-out above never waits on it.
    if (refreshToken) void api.logout(refreshToken).catch(() => {});
  }, [signOutLocally]);

  // installAndReload makes next the signed-in account and parks the one that
  // was: the same swap-and-reload as a tenant switch, which restarts every
  // singleton (token, acting tenant, caches, streams).
  const installAndReload = useCallback((next: SessionRecord) => {
    const gateway = gatewayBase();
    const current = storedSession();
    let list = unpark(readAccounts(), gateway, next.subject);
    if (current && current.subject !== next.subject) list = park(list, gateway, current);
    writeAccounts(list);
    persistSession(next);
    clearQueryCache();
    forgetLocalAgent();
    void forgetBrowserPush().catch(() => {});
    window.location.reload();
  }, []);

  const mintDeps = useMemo(() => ({ refresh: api.refresh, exchangeApiToken: api.exchangeApiToken }), []);

  const switchAccount = useCallback(
    async (key: string) => {
      const gateway = gatewayBase();
      const target = accountsOn(readAccounts(), gateway).find((a) => keyOf(a) === key);
      if (!target) throw new Error("That account is not in this browser any more");
      const mint = credentialMint(target.record, mintDeps);
      if (!mint) throw new Error("That account cannot sign in again");
      let record: SessionRecord;
      try {
        record = recordFromGrant(await mint());
      } catch (e) {
        // Refused: its sign-in ended or its token was revoked. Drop it; the
        // signed-in account stays as it is.
        const status = errorStatus(e);
        if (status === 401 || status === 403) {
          saveParked(unpark(readAccounts(), gateway, target.record.subject));
          throw new Error("That account can no longer sign in, so it was removed");
        }
        throw e;
      }
      installAndReload(record);
    },
    [installAndReload, mintDeps, saveParked],
  );

  const addAccount = useCallback(() => {
    const current = storedSession();
    if (current) saveParked(park(readAccounts(), gatewayBase(), current));
    // The parked account's sign-in stays valid: a local sign-out only.
    signOutLocally();
  }, [saveParked, signOutLocally]);

  const addTestAccount = useCallback(
    async (apiToken: string, activate: boolean) => {
      const token = apiToken.trim();
      const record = recordFromGrant({ ...(await api.exchangeApiToken(token)), apiToken: token });
      if (record.subject === storedSession()?.subject) throw new Error("You are already signed in as this account");
      if (activate) {
        installAndReload(record);
        return;
      }
      saveParked(park(readAccounts(), gatewayBase(), record));
    },
    [installAndReload, saveParked],
  );

  const removeAccount = useCallback(
    async (key: string) => {
      const gateway = gatewayBase();
      const target = accountsOn(readAccounts(), gateway).find((a) => keyOf(a) === key);
      if (!target) return;
      // A person's sign-in is revoked at the gateway; a test account's API
      // token stays valid until it is revoked in the platform console.
      if (target.record.refreshToken) void api.logout(target.record.refreshToken).catch(() => {});
      saveParked(unpark(readAccounts(), gateway, target.record.subject));
    },
    [saveParked],
  );

  // Another window of this browser signed in as someone else (or out): this
  // one shows the wrong account until it reloads; a renewal of the same
  // account reloads nothing. The parked list is shared too.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key === SESSION_KEY && sessionSubjectChanged(e.oldValue, e.newValue)) window.location.reload();
      else if (e.key === StorageKey.accounts) setParked(readAccounts());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
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
        exchangeApiToken: api.exchangeApiToken,
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
    if (!stored?.refreshToken && !stored?.apiToken) return;
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

  const multiAccount = !inDesktopShell();
  // Read from storage: the record changes with the subject and every token swap.
  const kind = useMemo<AccountKind>(() => accountKind(storedSession() ?? { token: "", refreshToken: null, apiToken: null, subject: null, tenant: null, role: null }), [subject, tokenEra]);
  const accounts = useMemo(() => accountsOn(parked, gatewayBase()), [parked]);
  const value = useMemo(
    () => ({
      subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra,
      login, switchTenant, logout, reauth,
      multiAccount, kind, accounts, switchAccount, addAccount, addTestAccount, removeAccount,
    }),
    [
      subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra,
      login, switchTenant, logout, reauth,
      multiAccount, kind, accounts, switchAccount, addAccount, addTestAccount, removeAccount,
    ],
  );
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}
