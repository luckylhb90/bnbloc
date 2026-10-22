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
import { openAccountWindow } from "@/lib/accountWindow";
import { bindGatewayBase, gatewayBase } from "@/lib/config";
import { isAuxWindow } from "@/lib/previewRoute";
import { renewAccount, withAccountLock, type RenewIo } from "@/lib/renewal";
import { visiblePoll } from "@/lib/useVisiblePoll";
import { bindActingTenant, clearActingTenant, getToken, refreshSession, setReauthHandler, setToken } from "@/lib/session";
import {
  parseSsoFragment,
  recordFromGrant,
  renewDelayMs,
  type Grant,
  type SessionRecord,
} from "@/lib/sessionGrant";
import { desktop, syncDesktopAccounts, takeDesktopSsoNonce } from "@/lib/desktop";
import { forgetBrowserPush } from "@/lib/webPush";
import { clearQueryCache } from "@/lib/useQuery";
import { forgetLocalAgent } from "@/lib/useLocalAgent";
import { StorageKey } from "@/lib/storageKeys";
import {
  accountKey,
  accountKind,
  find,
  keyOf,
  markSignedOut,
  put,
  readVault,
  remove,
  touch,
  updateVault,
  withRecord,
  withView,
  type AccountKind,
  type VaultAccount,
} from "@/lib/vault";
import { parseIntent, readBinding, resolveBinding, writeBinding } from "@/lib/windowBinding";

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
  // Signs this window's account out everywhere: the vault forgets it, its
  // sign-in is revoked at the gateway, every window showing it signs out.
  logout: () => void;
  reauth: () => Promise<void>;
  // Whether this window's account is a person who signed in or a test account.
  kind: AccountKind;
  // Every account of the vault, this window's included, and which one this
  // window shows (docs/prd/2026-10-22-multi-account.md).
  accounts: readonly VaultAccount[];
  current: string | null;
  // Shows another account in THIS window (renews it, re-binds, reloads).
  switchAccount: (key: string) => Promise<void>;
  // Opens an account in a window of its own, so two are side by side.
  openAccount: (key: string) => Promise<void>;
  // A window to sign in another account; this window is untouched.
  addAccount: () => void;
  // Adds a test account by its API token; activate shows it in this window.
  addTestAccount: (apiToken: string, activate: boolean) => Promise<void>;
  // `deleteLocalData` (the desktop only) also forgets what this computer keeps
  // of the account: its settings, sign-ins to tools, sandbox disks.
  removeAccount: (key: string, deleteLocalData?: boolean) => Promise<void>;
}

// The intent this window was opened with (lib/windowBinding.ts), read ONCE
// when the module loads and taken off the URL: the boot effect may run twice
// (React's strict mode in development), and the second run must not find the
// window opened for nothing in particular.
const bootIntent = (() => {
  if (typeof window === "undefined") return null;
  const { intent, rest } = parseIntent(window.location.hash);
  if (intent) window.history.replaceState(null, "", window.location.pathname + window.location.search + rest);
  return intent;
})();

const AuthCtx = createContext<AuthState>(null!);
export const useAuth = () => useContext(AuthCtx);

// The accounts live in the vault (lib/vault.ts, localStorage: reopening the
// app lands you signed in as the account you used last); a window shows the
// one it is bound to (lib/windowBinding.ts). The 1h session bearer is renewed
// from the account's refresh token (or a test account's API token); Sign Out
// forgets the account and revokes that sign-in.

// renewIo is the renewal's view of one account in the vault: its record, and
// the credential calls on ITS gateway (not the window's) with no session
// headers of the window's.
function renewIo(key: string): RenewIo {
  const gateway = find(readVault(), key)?.gateway ?? gatewayBase();
  return {
    read: () => find(readVault(), key)?.record ?? null,
    write: (record) => void updateVault((list) => withRecord(list, key, record)),
    markSignedOut: () => void updateVault((list) => markSignedOut(list, key)),
    now: Date.now,
    mintDeps: {
      refresh: (refreshToken) => api.refresh(refreshToken, gateway),
      exchangeApiToken: (apiToken) => api.exchangeApiToken(apiToken, gateway),
    },
  };
}

// renewUnderLock is the ONE renewal of an account: under its lock, from the
// vault's record, adopting a sibling window's renewal instead of minting again.
function renewUnderLock(key: string, have: string | null) {
  return withAccountLock(key, () => renewAccount(renewIo(key), have));
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
  const [accounts, setAccounts] = useState<VaultAccount[]>(() => readVault());
  // The account this window shows (its key), and the same for readers that
  // must see the current one, not a stale closure.
  const [current, setCurrent] = useState<string | null>(null);
  const currentRef = useRef<string | null>(null);
  currentRef.current = current;
  const tenantRef = useRef<string | null>(null);
  tenantRef.current = tenant;

  const syncAccounts = useCallback(() => setAccounts(readVault()), []);

  // install makes record the session of this window: bearer, acting tenant,
  // state, and a tokenEra bump so the desktop shell is handed what the vault
  // holds now.
  const install = useCallback((record: SessionRecord) => {
    setToken(record.token);
    bindActingTenant(record.subject);
    setTokenEra((n) => n + 1);
    setSubject(record.subject);
    setTenant(record.tenant);
    setRole(record.role);
  }, []);

  // bind shows the vault's account `key` in this window.
  const bind = useCallback(
    (account: VaultAccount) => {
      const key = keyOf(account);
      bindGatewayBase(account.gateway);
      writeBinding(key);
      setCurrent(key);
      install(account.record);
    },
    [install],
  );

  // adopt makes a freshly signed-in account the one this window shows: into
  // the vault (used now, signed in again), then bound.
  const adopt = useCallback(
    (record: SessionRecord) => {
      if (!record.subject || !(record.refreshToken || record.apiToken)) {
        throw new Error("The gateway issued a session that cannot be renewed");
      }
      const gateway = gatewayBase();
      const list = updateVault((l) => put(l, gateway, record, Date.now()));
      setAccounts(list);
      const account = find(list, accountKey(gateway, record.subject));
      if (account) bind(account);
    },
    [bind],
  );

  // signOutLocally unbinds this window: it shows nobody, the next account it
  // shows never reads this one's cached answers, and this browser stops
  // receiving its push notifications. The vault is not touched.
  const signOutLocally = useCallback(() => {
    clearActingTenant();
    clearQueryCache();
    forgetLocalAgent();
    setToken(null);
    setSubject(null);
    setTenant(null);
    setRole(null);
    setCurrent(null);
    writeBinding(null);
    void forgetBrowserPush().catch(() => {});
  }, []);

  useEffect(() => {
    // What this window shows: the account it was opened for, the one it was
    // already bound to (a reload), else the one used last. Its gateway is this
    // window's before anything is asked of one.
    const vault = readVault();
    const account = find(vault, resolveBinding({ stored: readBinding(), intent: bootIntent, vault }).key);
    if (account) bindGatewayBase(account.gateway);
    api
      .ssoConfig()
      .then((c) => {
        setSsoAvailable(!!c.authProviderAvailable);
        setDirectLoginAvailable(c.directLoginAvailable !== false);
        setSsoStartPath(c.authProviderStartPath ?? null);
      })
      .catch(() => {});
    // Every sign-in path lands through signedIn() with its refresh token.
    const signedIn = (g: Grant) => {
      try {
        adopt(recordFromGrant(g));
      } catch {
        /* a session that cannot be kept: stay on the sign-in */
      }
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
          .then(signedIn)
          .catch(() => setReady(true));
        return;
      }
    }
    if (fragment.grant) {
      window.history.replaceState(null, "", "/");
      signedIn(fragment.grant);
      return;
    }
    if (account) {
      updateVault((l) => touch(l, keyOf(account), Date.now()));
      bind(account);
      // A window that was not opened for a place of its own goes where the
      // account last was.
      const hash = window.location.hash;
      if (account.lastView && (!hash || hash === "#" || hash === "#/") && !isAuxWindow()) {
        window.history.replaceState(null, "", window.location.pathname + window.location.search + account.lastView);
      }
    } else {
      writeBinding(null);
    }
    setReady(true);
  }, [adopt, bind]);

  // Mirrors of the session scope for the re-auth handler, which is installed
  // once and must read the CURRENT account, not a stale closure.
  const login = useCallback(
    async (idpToken: string) => {
      adopt(recordFromGrant(await api.login(idpToken)));
    },
    [adopt],
  );

  const switchTenant = useCallback(async (tenantId: string) => {
    const { sessionToken, subject, tenant, role } = await api.switchTenant(tenantId);
    const key = currentRef.current;
    const held = find(readVault(), key)?.record;
    // switch-tenant mints no refresh token: the sign-in's stays, and its
    // family now refreshes into the switched tenant (server-side).
    const next: SessionRecord = {
      token: sessionToken,
      refreshToken: held?.refreshToken ?? null,
      apiToken: held?.apiToken ?? null,
      subject,
      tenant: tenant ?? null,
      role: role ?? null,
    };
    if (key) updateVault((l) => withRecord(l, key, next));
    setToken(sessionToken);
    setSubject(subject);
    setTenant(next.tenant);
    setRole(next.role);
    // Rooms/agents/RBAC are all scoped to the token's tenant; reload so every
    // view re-fetches under the new scope instead of showing stale data.
    clearQueryCache();
    window.location.reload();
  }, []);

  // removeAccount forgets an account: its sign-in is revoked at the gateway (a
  // person's; a test account's API token stays valid until it is revoked in
  // the platform console), the vault drops it, and every window that shows it
  // signs out (the storage event below; this one right here).
  const removeAccount = useCallback(
    async (key: string, deleteLocalData = false) => {
      const target = find(readVault(), key);
      if (!target) return;
      if (target.record.refreshToken) void api.logout(target.record.refreshToken, target.gateway).catch(() => {});
      setAccounts(updateVault((l) => remove(l, key)));
      if (key === currentRef.current) signOutLocally();
      if (deleteLocalData && desktop.available()) {
        // The shell stops the account when it is handed the vault without it,
        // then forgets its data.
        await syncDesktopAccounts();
        await desktop.forgetAccountData(target.gateway, target.record.subject ?? "");
      }
    },
    [signOutLocally],
  );

  const logout = useCallback(() => {
    const key = currentRef.current;
    if (key) void removeAccount(key);
    else signOutLocally();
  }, [removeAccount, signOutLocally]);

  // switchAccount shows another account in THIS window: renew it (from the
  // vault's record, under its lock), re-bind, go where it last was, reload:
  // every singleton of the page (bearer, acting tenant, caches, streams,
  // local-agent store) belongs to an account, and restarting them is the only
  // way to be sure nothing of the old account shows in the new one.
  const switchAccount = useCallback(
    async (key: string) => {
      if (!find(readVault(), key)) throw new Error("That account is not in this app any more");
      const result = await renewUnderLock(key, null);
      syncAccounts();
      if (result.outcome === "refused") throw new Error("That account can no longer sign in: sign in again");
      if (result.outcome === "failed") throw new Error("Could not reach the gateway to sign in as that account");
      if (result.outcome === "none") throw new Error("That account cannot sign in again");
      const list = updateVault((l) => touch(l, key, Date.now()));
      writeBinding(key);
      clearQueryCache();
      forgetLocalAgent();
      void forgetBrowserPush().catch(() => {});
      window.location.hash = find(list, key)?.lastView ?? "";
      window.location.reload();
    },
    [syncAccounts],
  );

  // openAccount shows an account in a window of its own; where none can be
  // opened it takes this window.
  const openAccount = useCallback(
    async (key: string) => {
      if (!(await openAccountWindow({ kind: "account", key }))) await switchAccount(key);
    },
    [switchAccount],
  );

  // addAccount opens a window with the sign-in and leaves this one as it is;
  // where none can be opened this window shows the sign-in (the account it
  // showed stays in the vault, signed in).
  const addAccount = useCallback(() => {
    void openAccountWindow({ kind: "add" }).then((opened) => {
      if (!opened) signOutLocally();
    });
  }, [signOutLocally]);

  const addTestAccount = useCallback(
    async (apiToken: string, activate: boolean) => {
      const token = apiToken.trim();
      const record = recordFromGrant({ ...(await api.exchangeApiToken(token)), apiToken: token });
      const gateway = gatewayBase();
      const key = accountKey(gateway, record.subject ?? "");
      if (key === currentRef.current) throw new Error("You are already signed in as this account");
      // Added, not used: it does not become the account a new window binds to.
      const list = updateVault((l) => put(l, gateway, record, activate ? Date.now() : (find(l, key)?.lastUsed ?? 0)));
      setAccounts(list);
      if (activate) await switchAccount(key);
    },
    [switchAccount],
  );

  // What other windows do to the vault: an account removed or refused (by any
  // window) signs this window out if it shows it; a renewal of the account
  // this window shows is adopted without a reload; the account moving to
  // another tenant reloads it.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key !== StorageKey.vault) return;
      const list = readVault();
      setAccounts(list);
      const key = currentRef.current;
      if (!key) return;
      const mine = find(list, key);
      if (!mine || mine.signedOut) {
        signOutLocally();
        return;
      }
      if (mine.record.tenant !== tenantRef.current) {
        window.location.reload();
        return;
      }
      if (mine.record.token !== getToken()) install(mine.record);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [install, signOutLocally]);

  // The ONE silent re-auth path. Every 401 recovery — api requests, uploads,
  // downloads, the SSE loop, an explicit join, the proactive renewal below —
  // funnels through session.refreshSession(), which coalesces concurrent
  // callers onto this handler. The handler renews the window's account under
  // its lock: a renewal a sibling window already made is adopted, otherwise
  // the vault's credential mints it. A refusal (401/403) marks the account
  // "sign in again" in the vault and takes this window to the sign-in instead
  // of rendering empty views with a dead token; a network failure keeps the
  // session so a later request retries. The gateway mints in the tenant the
  // member chose (it persists on the sign-in's refresh family).
  useEffect(() => {
    setReauthHandler(async () => {
      const key = currentRef.current;
      if (!key) return false;
      const result = await renewUnderLock(key, getToken());
      syncAccounts();
      if (result.outcome === "fresh" || result.outcome === "renewed") {
        install(result.record);
        return true;
      }
      if (result.outcome === "refused") signOutLocally();
      return false;
    });
    return () => setReauthHandler(null);
  }, [install, signOutLocally, syncAccounts]);

  // Proactive renewal: one timer renews the session shortly before its bearer
  // expires, so long-lived SSE streams and uploads never hit a dead token.
  // Re-armed on every token swap (tokenEra) and cleared on sign-out/unmount.
  useEffect(() => {
    if (!current) return;
    const held = find(readVault(), current)?.record;
    if (!held) return;
    const delay = renewDelayMs(held.token, Date.now());
    if (delay === null) return;
    const timer = window.setTimeout(() => void refreshSession(), delay);
    return () => window.clearTimeout(timer);
  }, [current, tokenEra]);

  // The keep-alive: every other account of the vault stays signed in while the
  // app is open (a refresh family ages out after 30 days unused). Each is
  // renewed, when it is due, under its own lock.
  useEffect(() => {
    if (!ready || isAuxWindow()) return;
    let stopped = false;
    const keepAlive = async () => {
      for (const account of readVault()) {
        const key = keyOf(account);
        if (stopped) return;
        if (key === currentRef.current || account.signedOut) continue;
        await renewUnderLock(key, null);
      }
      if (!stopped) syncAccounts();
    };
    void keepAlive();
    const stop = visiblePoll(() => void keepAlive(), KEEP_ALIVE_EVERY_MS);
    return () => {
      stopped = true;
      stop();
    };
  }, [ready, syncAccounts]);

  // Where the account last was is where a window binding to it again goes.
  useEffect(() => {
    if (!current || isAuxWindow()) return;
    let timer: number | undefined;
    const save = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void updateVault((l) => withView(l, current, window.location.hash)), 400);
    };
    save();
    window.addEventListener("hashchange", save);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("hashchange", save);
    };
  }, [current]);

  // reauth is the context-facing form of the same refresh; it throws when the
  // session could not be renewed so callers (join) surface the failure.
  const reauth = useCallback(async () => {
    if (!(await refreshSession())) throw new Error("Could not refresh your session");
  }, []);

  const kind = useMemo<AccountKind>(() => {
    const held = find(accounts, current)?.record;
    return held ? accountKind(held) : "member";
  }, [accounts, current]);
  const value = useMemo(
    () => ({
      subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra,
      login, switchTenant, logout, reauth,
      kind, accounts, current, switchAccount, openAccount, addAccount, addTestAccount, removeAccount,
    }),
    [
      subject, tenant, role, ready, ssoAvailable, directLoginAvailable, ssoStartPath, tokenEra,
      login, switchTenant, logout, reauth,
      kind, accounts, current, switchAccount, openAccount, addAccount, addTestAccount, removeAccount,
    ],
  );
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

// How often the keep-alive looks at the accounts no window of this one shows.
const KEEP_ALIVE_EVERY_MS = 30 * 60 * 1000;
