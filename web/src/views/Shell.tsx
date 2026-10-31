import { useEffect, useMemo, useRef, useState } from "react";
import { Select } from "@/ui/Select";
import {
  SquaresFourIcon,
  ChatsCircleIcon,
  ListIcon,
  SidebarSimpleIcon,
  XIcon,
  WaveformIcon,
  GlobeIcon,
  LockIcon,
  BooksIcon,
  MagnifyingGlassIcon,
  BuildingsIcon,
  FolderOpenIcon,
} from "@phosphor-icons/react";
import { api, type RoomSummary } from "@/lib/api";
import { useActingTenant } from "@/lib/session";
import { useTenants } from "@/lib/queries";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { useStage } from "@/lib/capability";
import { useModalLayer } from "@/ui/useModalLayer";
import { AccountMenu } from "@/views/AccountMenu";
import { notify, requestNotificationPermission } from "@/lib/notify";
import { openGlobalSearch } from "@/lib/team";
import { localFiles } from "@/lib/localFiles";
import { requestLocalOpen } from "@/views/LocalFileOpener";
import { onRoomGone, withoutRoom } from "@/lib/roomGone";
import { watchUnread } from "@/lib/sse";
import { applyUnreadDelta, noUnread, unreadFromState, type UnreadCounts } from "@/lib/unreadActivity";
import { can } from "@/lib/permissions";
import { StorageKey, readStored, writeStored } from "@/lib/storageKeys";

export type View = "plaza" | "myrooms" | "agents" | "team" | "board" | "components" | "org" | "directory" | "releases" | "library" | "settings";

export function Shell({
  view,
  activeRoom,
  onView,
  onOpenRoom,
  footerNotice,
  children,
}: {
  view: View;
  activeRoom: string | null;
  onView: (v: View) => void;
  onOpenRoom: (id: string) => void;
  // Optional quiet notice rendered above the account footer (e.g. an available
  // desktop update). Receives whether the sidebar is collapsed, and a way to
  // close the compact drawer before the notice navigates.
  footerNotice?: (collapsed: boolean, closeDrawer: () => void) => React.ReactNode;
  children: React.ReactNode;
}) {
  const { subject, role, tenant, logout, switchTenant: reScopeSession } = useAuth();
  const shellRef = useRef<HTMLDivElement | null>(null);
  const shellStage = useStage(shellRef);
  const [open, setOpen] = useState(false);
  // The room publishes `body.dock-open` while a preview is docked inline: the
  // chat column is under space pressure, so the shell sheds its labels too.
  // It is a cross-component signal, so it cannot be a container measurement —
  // but it only ever *adds* collapse on top of what the stage already decides.
  const [previewDocked, setPreviewDocked] = useState(false);
  useEffect(() => {
    const apply = () => setPreviewDocked(document.body.classList.contains("dock-open"));
    apply();
    const mo = new MutationObserver(apply);
    mo.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    return () => mo.disconnect();
  }, []);
  const sidebarInline = shellStage !== "compact";
  // Collapse has three inputs, in priority order: the user's explicit choice
  // wins, but a forced collapse still applies when the space genuinely is not
  // there (medium stage, or a preview docked inline) — honouring "expanded"
  // then would crush the chat column the sidebar sits next to.
  const [userCollapsed, setUserCollapsed] = useState(() => {
    return readStored(StorageKey.sidebarCollapsed) === "1";
  });
  const forcedCollapsed = previewDocked || shellStage === "medium";
  // Compact: the sidebar is a full-label drawer over the page, never a rail.
  const sidebarCollapsed = sidebarInline && (forcedCollapsed || userCollapsed);
  const drawerOpen = !sidebarInline && open;
  const asideRef = useRef<HTMLElement | null>(null);
  useModalLayer(asideRef, { open: drawerOpen, onEscape: () => setOpen(false) });
  const toggleSidebar = () => {
    setUserCollapsed((prev) => {
      const next = !prev;
      writeStored(StorageKey.sidebarCollapsed, next ? "1" : "0");
      return next;
    });
  };
  const [rooms, setRooms] = useState<RoomSummary[]>([]);
  const isSuper = can(role, "manage_platform");
  // Super-admin global tenant switch: the tenant the whole session acts as
  // (lib/session.ts owns it; switching reloads so every view re-fetches under
  // the new scope).
  const { acting, switchTo: switchActingTenant } = useActingTenant(isSuper);
  const tenants = useTenants(isSuper).data ?? [];

  // Ordinary members of multiple orgs get a workspace switcher too. Unlike the
  // super-admin path (a header overlay), this re-issues the session token for
  // the chosen tenant via auth.switchTenant, so role + rooms follow. We source
  // the list from the directory (relation === "member").
  const [myTenants, setMyTenants] = useState<{ tenantId: string; name: string }[]>([]);
  useEffect(() => {
    if (isSuper) return; // super-admin uses the acting-tenant switcher above
    api
      .tenantDirectory()
      .then((r) => setMyTenants((r.tenants ?? []).filter((t) => t.relation === "member")))
      .catch(() => {});
  }, [isSuper]);

  // Quick-access room list in the sidebar (recent rooms), refreshed when the
  // view changes or a room opens so newly created/joined rooms appear.
  useEffect(() => {
    if (!subject) return;
    api
      .myRooms(subject)
      .then((r) => setRooms(r.rooms || []))
      // A failed refresh keeps the last list: blanking it would claim the
      // member has no rooms (web L13).
      .catch(() => {});
  }, [subject, view, activeRoom]);

  // A room another surface found gone (detail 404/403, stream revoked) leaves
  // the quick-access list at once, which also drops it from the unread stream.
  useEffect(() => onRoomGone((id) => setRooms((cur) => withoutRoom(cur, id))), []);

  // Per-room unread counts, pushed by the activity stream so the sidebar
  // badges and the document title stay live without opening every room. `unreadError`
  // distinguishes a failed fetch from a genuine zero so we never silently claim
  // "all read" when the count is actually unknown.
  const [unread, setUnread] = useState<Record<string, number>>({});
  const [unreadError, setUnreadError] = useState(false);
  // Total unread across the last update, kept in a ref so the stream can
  // detect an increase (new message) and fire a background notification without
  // re-subscribing on every change.
  const prevTotalRef = useRef(0);
  // The document title we own, restored verbatim when unread drops to zero.
  const baseTitleRef = useRef<string>(typeof document !== "undefined" ? document.title : "");

  // Ask for notification permission on the member's first interaction after
  // signing in: browsers ignore (or penalize) permission prompts that are not
  // triggered by a user gesture. Denied/unsupported makes notify() a no-op.
  useEffect(() => {
    if (!subject) return;
    const ask = () => void requestNotificationPermission();
    window.addEventListener("pointerdown", ask, { once: true });
    window.addEventListener("keydown", ask, { once: true });
    return () => {
      window.removeEventListener("pointerdown", ask);
      window.removeEventListener("keydown", ask);
    };
  }, [subject]);

  // The stream is keyed on the SET of room ids (a refetched but identical
  // room list must not reopen it), and reads names / the open callback
  // through refs so a parent re-render never re-runs it.
  const roomIdsKey = useMemo(() => rooms.map((r) => r.roomId).join("\u0000"), [rooms]);
  const roomsRef = useRef(rooms);
  roomsRef.current = rooms;
  const onOpenRoomRef = useRef(onOpenRoom);
  onOpenRoomRef.current = onOpenRoom;

  // Unread counts for the known rooms, pushed by the tab's member stream
  // (sse.watchUnread): their state on every connection, then what changes.
  // A room marked read on any device is read again; every few minutes all
  // are, as a safety net. A rising total while the tab is hidden fires a
  // desktop notification; the title reflects the current total.
  useEffect(() => {
    if (!subject) return;
    const ids = roomIdsKey ? roomIdsKey.split("\u0000") : [];
    if (ids.length === 0) {
      setUnread({});
      setUnreadError(false);
      prevTotalRef.current = 0;
      return;
    }
    let cancelled = false;
    let current: UnreadCounts = noUnread;
    const publish = (next: UnreadCounts) => {
      if (cancelled) return;
      current = next;
      const counts = next.counts;
      setUnread({ ...counts });
      const total = Object.values(counts).reduce((a, b) => a + (b || 0), 0);
      if (total > prevTotalRef.current) {
        // Name the room with new activity when there's exactly one, else a
        // generic prompt. notify() self-gates on tab-hidden + permission.
        const grew = ids.filter((id) => (counts[id] || 0) > 0);
        const label =
          grew.length === 1
            ? roomsRef.current.find((r) => r.roomId === grew[0])?.name || grew[0]
            : `${total} unread messages`;
        notify("New activity in bnbloc", {
          body: grew.length === 1 ? `New messages in ${label}` : label,
          tag: "bnbloc-unread",
          onClick: () => grew.length === 1 && onOpenRoomRef.current(grew[0]),
        });
      }
      prevTotalRef.current = total;
    };
    const reread = async (rooms: string[]) => {
      try {
        const res = await api.roomsUnread(rooms);
        publish(unreadFromState(res, current, rooms));
        if (!cancelled) setUnreadError(false);
      } catch {
        if (!cancelled) setUnreadError(true);
      }
    };
    const close = watchUnread(ids, {
      onState: (state) => {
        publish(unreadFromState(state, current));
        if (!cancelled) setUnreadError(false);
      },
      onDelta: (delta) => {
        const { next, reread: again } = applyUnreadDelta(current, delta);
        publish(next);
        if (again.length > 0) void reread(again);
      },
      onStatus: (up) => {
        if (!cancelled && !up) setUnreadError(true);
      },
    });
    const trueUp = window.setInterval(() => void reread(ids), 5 * 60_000);
    return () => {
      cancelled = true;
      clearInterval(trueUp);
      close();
    };
  }, [subject, roomIdsKey]);

  // Reflect total unread in the document title, e.g. "(3) bnbloc", restoring
  // the original title when everything is read.
  useEffect(() => {
    const total = Object.values(unread).reduce((a, b) => a + (b || 0), 0);
    document.title = total > 0 ? `(${total > 99 ? "99+" : total}) ${baseTitleRef.current}` : baseTitleRef.current;
    return () => {
      document.title = baseTitleRef.current;
    };
  }, [unread]);

  // The everyday core stays on top; every other destination lives in the
  // account menu at the bottom (AccountMenu).
  const nav = [
    { id: "myrooms" as const, label: "My rooms", icon: ChatsCircleIcon },
    { id: "plaza" as const, label: "Plaza", icon: SquaresFourIcon },
    { id: "library" as const, label: "Library", icon: BooksIcon },
  ];

  return (
    <div
      ref={shellRef}
      className={cn(
        "coarse-targets grid h-[var(--app-height,100dvh)]",
        sidebarInline
          ? "grid-cols-[auto_minmax(0,1fr)] grid-rows-1"
          : "grid-rows-[auto_minmax(0,1fr)]",
      )}
      data-shell-stage={shellStage}
    >
      {/* Mobile top bar */}
      <div className={cn("pad-safe pad-safe-t items-center justify-between border-b border-[var(--color-border)] px-4 py-3 [--pad-safe-x:1rem] [--pad-safe-y:0.75rem]", sidebarInline ? "hidden" : "flex")}>
        <div className="flex items-center gap-2">
          <WaveformIcon size={18} weight="fill" className="text-[var(--color-accent)]" />
          <span className="font-semibold tracking-tight">bnbloc</span>
        </div>
        <button
          className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
          onClick={() => setOpen((o) => !o)}
          aria-label={open ? "Close menu" : "Open menu"}
          aria-expanded={open}
        >
          {open ? <XIcon size={20} /> : <ListIcon size={20} />}
        </button>
      </div>

      {/* Compact: the sidebar opens as a drawer over a dimmed page. */}
      {drawerOpen && (
        <button
          type="button"
          aria-label="Close menu"
          tabIndex={-1}
          className="fixed inset-0 z-40 bg-black/60 animate-in"
          onClick={() => setOpen(false)}
        />
      )}
      {/* Sidebar */}
      <aside
        ref={asideRef}
        role={drawerOpen ? "dialog" : undefined}
        aria-modal={drawerOpen || undefined}
        aria-label={drawerOpen ? "Menu" : undefined}
        className={cn(
          "flex-col border-r border-[var(--color-border)] bg-[var(--color-surface)] shell-sidebar",
          sidebarInline
            ? sidebarCollapsed ? "w-[64px] p-2" : "w-[236px] p-3"
            : "pad-safe-t pad-safe-b fixed inset-y-0 left-0 z-50 w-[min(85vw,18rem)] overflow-y-auto p-3 shadow-[var(--shadow-diffuse)] [--pad-safe-y:0.75rem] animate-in",
          "transition-[width,padding] duration-200 ease-out",
          sidebarInline || open ? "flex" : "hidden",
        )}
      >
        <div className={cn("items-center gap-2 py-3", sidebarInline ? "flex" : "hidden", sidebarCollapsed ? "justify-center" : "px-3")}>
          <WaveformIcon size={20} weight="fill" className="shrink-0 text-[var(--color-accent)]" />
          {!sidebarCollapsed && <span className="text-base font-semibold tracking-tight">bnbloc</span>}
          {!sidebarCollapsed && (
            <button
              type="button"
              onClick={toggleSidebar}
              title="Collapse sidebar"
              aria-label="Collapse sidebar"
              aria-expanded={!sidebarCollapsed}
              className="tap tactile ml-auto grid size-7 shrink-0 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
            >
              <SidebarSimpleIcon size={16} />
            </button>
          )}
        </div>

        {/* Expanding is only offered when the user's choice is what is keeping
            the sidebar narrow; while forcedCollapsed there is no room to give. */}
        {sidebarCollapsed && sidebarInline && !forcedCollapsed && (
          <button
            type="button"
            onClick={toggleSidebar}
            title="Expand sidebar"
            aria-label="Expand sidebar"
            aria-expanded={false}
            className="tap tactile mx-auto mb-1 grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
          >
            <SidebarSimpleIcon size={16} />
          </button>
        )}

        <nav className="mt-2 flex flex-col gap-1">
          {/* Global search (also Cmd/Ctrl+K). */}
          <button
            onClick={() => {
              openGlobalSearch();
              setOpen(false);
            }}
            title="Search (Ctrl+K)"
            className={cn(
              "tactile flex items-center rounded-[var(--radius)] text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
              sidebarCollapsed ? "relative h-10 w-10 justify-center" : "gap-3 px-3 py-2",
            )}
          >
            <MagnifyingGlassIcon size={sidebarCollapsed ? 19 : 18} className="shrink-0" />
            {!sidebarCollapsed && <span className="flex-1 text-left">Search</span>}
            {!sidebarCollapsed && (
              <kbd className="rounded border border-[var(--color-border)] px-1 text-[0.6rem] text-[var(--color-fg-subtle)]">⌘K</kbd>
            )}
          </button>
          {/* A document from this computer, opened with AI (desktop only, also Cmd/Ctrl+O). */}
          {localFiles.available() && (
            <button
              onClick={() => {
                requestLocalOpen();
                setOpen(false);
              }}
              title="Open a file (Ctrl+O)"
              className={cn(
                "tactile flex items-center rounded-[var(--radius)] text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                sidebarCollapsed ? "relative h-10 w-10 justify-center" : "gap-3 px-3 py-2",
              )}
            >
              <FolderOpenIcon size={sidebarCollapsed ? 19 : 18} className="shrink-0" />
              {!sidebarCollapsed && <span className="flex-1 text-left">Open a file…</span>}
              {!sidebarCollapsed && (
                <kbd className="rounded border border-[var(--color-border)] px-1 text-[0.6rem] text-[var(--color-fg-subtle)]">⌘O</kbd>
              )}
            </button>
          )}
          {nav.map((n) => {
            const active = view === n.id;
            return (
              <button
                key={n.id}
                onClick={() => {
                  onView(n.id);
                  setOpen(false);
                }}
                title={n.label}
                className={cn(
                  "tactile flex items-center rounded-[var(--radius)] text-sm",
                  sidebarCollapsed
                    ? "relative h-10 w-10 justify-center"
                    : "gap-3 px-3 py-2",
                  active
                    ? "bg-[var(--color-elevated)] text-[var(--color-fg)] font-medium"
                    : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]",
                )}
              >
                <n.icon size={sidebarCollapsed ? 19 : 18} weight={active ? "fill" : "regular"} className="shrink-0" />
                {!sidebarCollapsed && <span className="flex-1 text-left">{n.label}</span>}
                {n.id === "myrooms" &&
                  (() => {
                    const totalUnread = Object.values(unread).reduce((a, b) => a + (b || 0), 0);
                    if (totalUnread > 0) {
                      if (sidebarCollapsed) {
                        return (
                          <span
                            className="absolute right-1 top-1 size-2.5 rounded-full bg-[var(--color-accent)]"
                            title={`${totalUnread} unread`}
                          />
                        );
                      }
                      return (
                        <span
                          className="tabular grid min-w-5 place-items-center rounded-full bg-[var(--color-accent)] px-1.5 text-xs font-semibold text-[var(--color-accent-fg,white)]"
                          title={`${totalUnread} unread`}
                        >
                          {totalUnread > 99 ? "99+" : totalUnread}
                        </span>
                      );
                    }
                    if (rooms.length > 0) {
                      // Collapsed is an icon rail: a neutral count is not worth
                      // a glyph. Unread (above) still earns its dot.
                      if (sidebarCollapsed) return null;
                      return (
                        <span className="tabular rounded-full bg-[var(--color-surface-2)] px-1.5 text-xs text-[var(--color-fg-subtle)]">
                          {rooms.length}
                        </span>
                      );
                    }
                    return null;
                  })()}
              </button>
            );
          })}
        </nav>

        {/* Super-admin global tenant switch: acts the whole session as the
            chosen tenant. Hidden for everyone else. */}
        {isSuper && tenants.length > 0 && (
          <div className={cn("mt-3 flex flex-col gap-1", sidebarCollapsed ? "items-center px-0" : "px-3")}>
            {!sidebarCollapsed && (
            <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
              Acting tenant
            </span>
            )}
            <div className={cn("relative", sidebarCollapsed ? "size-9" : "w-full")}>
              {sidebarCollapsed && (
                <div
                  className="pointer-events-none grid size-9 place-items-center rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-fg-muted)]"
                  title={`Acting tenant: ${acting || tenant}`}
                >
                  <BuildingsIcon size={17} />
                </div>
              )}
              <Select
                value={acting ?? ""}
                onChange={(e) => switchActingTenant(e.target.value || null)}
                title={`Acting tenant: ${acting || tenant}`}
                className={cn(
                  "text-sm",
                  sidebarCollapsed
                    ? "absolute inset-0 size-9 cursor-pointer appearance-none border-0 bg-transparent p-0 text-transparent"
                    : "w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5",
                )}
              >
                <option value="">(home: {tenant})</option>
                {tenants.map((t) => (
                  <option key={t.tenantId} value={t.tenantId}>
                    {t.name || t.tenantId}
                    {t.status === "suspended" ? " (suspended)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          </div>
        )}

        {/* Member of multiple orgs: switch the active workspace. Re-issues the
            session for the chosen tenant (role + rooms follow). Only shown when
            the user actually belongs to more than one org. */}
        {!isSuper && myTenants.length > 1 && (
          <div className={cn("mt-3 flex flex-col gap-1", sidebarCollapsed ? "items-center px-0" : "px-3")}>
            {!sidebarCollapsed && (
            <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
              Workspace
            </span>
            )}
            <div className={cn("relative", sidebarCollapsed ? "size-9" : "w-full")}>
              {sidebarCollapsed && (
                <div
                  className="pointer-events-none grid size-9 place-items-center rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-fg-muted)]"
                  title={`Workspace: ${myTenants.find((t) => t.tenantId === tenant)?.name || tenant || ""}`}
                >
                  <BuildingsIcon size={17} />
                </div>
              )}
              <Select
                value={tenant ?? ""}
                onChange={(e) => {
                  if (e.target.value && e.target.value !== tenant) void reScopeSession(e.target.value);
                }}
                title={`Workspace: ${myTenants.find((t) => t.tenantId === tenant)?.name || tenant || ""}`}
                className={cn(
                  "text-sm",
                  sidebarCollapsed
                    ? "absolute inset-0 size-9 cursor-pointer appearance-none border-0 bg-transparent p-0 text-transparent"
                    : "w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5",
                )}
              >
                {myTenants.map((t) => (
                  <option key={t.tenantId} value={t.tenantId}>
                    {t.name || t.tenantId}
                  </option>
                ))}
              </Select>
            </div>
          </div>
        )}

        {rooms.length > 0 && (
          <div className="mt-5 min-h-0 flex-1 overflow-y-auto">
            <div className={cn("flex items-center gap-1.5 pb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]", sidebarCollapsed ? "justify-center px-0" : "px-3")}>
              {!sidebarCollapsed && <span>Quick access</span>}
              {unreadError && (
                <span
                  className="inline-block size-1.5 rounded-full bg-[var(--color-warning,#d97706)]"
                  title="Unread counts unavailable — retrying"
                />
              )}
            </div>
            <div className="flex flex-col gap-0.5">
              {rooms.slice(0, 8).map((rm) => {
                const active = rm.roomId === activeRoom;
                const roomUnread = unread[rm.roomId] || 0;
                return (
                  <button
                    key={rm.roomId}
                    onClick={() => {
                      onOpenRoom(rm.roomId);
                      setOpen(false);
                    }}
                    title={rm.name || rm.roomId}
                    className={cn(
                      "tactile flex items-center rounded-[var(--radius)] text-sm",
                       sidebarCollapsed ? "relative h-9 w-9 justify-center" : "gap-2 px-3 py-1.5",
                      active
                        ? "bg-[var(--color-elevated)] text-[var(--color-fg)]"
                        : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]",
                    )}
                  >
                    {rm.visibility === "shared" ? (
                      <GlobeIcon
                         size={sidebarCollapsed ? 16 : 13}
                         className={cn("shrink-0", active && sidebarCollapsed ? "text-[var(--color-accent)]" : "text-[var(--color-fg-subtle)]")}
                      />
                    ) : (
                      <LockIcon
                         size={sidebarCollapsed ? 16 : 13}
                         className={cn("shrink-0", active && sidebarCollapsed ? "text-[var(--color-accent)]" : "text-[var(--color-fg-subtle)]")}
                      />
                    )}
                    {!sidebarCollapsed && <span className="flex-1 truncate text-left">{rm.name || rm.roomId}</span>}
                    {roomUnread > 0 &&
                      (sidebarCollapsed ? (
                        <span
                          className="absolute right-0.5 top-0.5 size-2 rounded-full bg-[var(--color-accent)]"
                          title={roomUnread + " unread"}
                        >
                        </span>
                      ) : (
                        <span
                          className="tabular grid min-w-4 shrink-0 place-items-center rounded-full bg-[var(--color-accent)] px-1 text-[0.65rem] font-semibold text-[var(--color-accent-fg,white)]"
                          title={roomUnread + " unread"}
                        >
                          {roomUnread > 99 ? "99+" : roomUnread}
                        </span>
                      ))}
                  </button>
                );
              })}
            </div>
          </div>
        )}

        <div className="mt-auto flex flex-col gap-2">
          {footerNotice?.(sidebarCollapsed, () => setOpen(false))}
          <AccountMenu
            subject={subject}
            role={role}
            view={view}
            collapsed={sidebarCollapsed}
            onView={(v) => {
              onView(v);
              setOpen(false);
            }}
            onSignOut={logout}
          />
        </div>
      </aside>

      {/* Main */}
      <main className="min-h-0 min-w-0 overflow-y-auto">{children}</main>
    </div>
  );
}
