import {
  BooksIcon,
  BuildingsIcon,
  CaretUpDownIcon,
  DownloadSimpleIcon,
  GearSixIcon,
  GlobeIcon,
  PackageIcon,
  SignOutIcon,
  UsersThreeIcon,
} from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { usePrivacyMode } from "@/lib/privacyMode";
import { useDisplayName } from "@/lib/useDisplayName";
import { Menu, MenuGroup, MenuItem } from "@/ui/Menu";
import { BuildVersion } from "@/views/BuildVersion";
import type { View } from "@/views/Shell";
import { can } from "@/lib/permissions";

// The views reached through the account menu rather than the sidebar's top
// nav. While one of them is open the account button shows the active state.
const MENU_VIEWS: ReadonlySet<View> = new Set<View>([
  "library",
  "team",
  "components",
  "org",
  "directory",
  "settings",
  "releases",
]);

// AccountMenu is the sidebar's single bottom account button (avatar + name, or
// the avatar alone on the collapsed rail). It opens upward with the secondary
// destinations grouped as Work / Workspace / Me, and the build stamp.
export function AccountMenu({
  subject,
  role,
  view,
  collapsed,
  onView,
  onSignOut,
}: {
  readonly subject: string | null;
  readonly role: string | null;
  readonly view: View;
  readonly collapsed: boolean;
  readonly onView: (v: View) => void;
  readonly onSignOut: () => void;
}) {
  const active = MENU_VIEWS.has(view);
  // The member's display name, never their id (PRD 2026-10-10 §1); the id is
  // shown as "Signed in as" only outside privacy mode.
  const name = useDisplayName(subject);
  const privacy = usePrivacyMode();
  const isAdmin = can(role, "manage_org");
  const item = (id: View, label: string, icon: Parameters<typeof MenuItem>[0]["icon"]) => (
    <MenuItem icon={icon} current={view === id} onSelect={() => onView(id)}>
      {label}
    </MenuItem>
  );
  return (
    <Menu
      label="Account"
      className={collapsed ? "self-center" : undefined}
      menuClassName="absolute bottom-full left-0 mb-2 max-h-[calc(var(--app-height,100dvh)-5rem)] w-60 max-w-[calc(100vw-1.5rem)]"
      trigger={(props, open) => (
        <button
          {...props}
          title={`${name || "Account"} — account menu`}
          aria-label={`Account menu (${name || "signed in"})`}
          className={cn(
            "tactile flex items-center rounded-[var(--radius)] text-sm",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
            collapsed ? "size-10 justify-center" : "w-full gap-2.5 px-2 py-1.5",
            active || open
              ? "bg-[var(--color-elevated)] text-[var(--color-fg)]"
              : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
          )}
        >
          <span
            aria-hidden
            className={cn(
              "grid size-8 shrink-0 place-items-center rounded-full bg-[var(--color-accent-soft)] text-sm font-semibold text-[var(--color-accent)]",
              active && "ring-2 ring-[var(--color-accent)]/70",
            )}
          >
            {(name[0] || "?").toUpperCase()}
          </span>
          {!collapsed && (
            <>
              <span className={cn("min-w-0 flex-1 truncate text-left", active && "font-medium")}>{name}</span>
              <CaretUpDownIcon size={14} className="shrink-0 text-[var(--color-fg-subtle)]" />
            </>
          )}
        </button>
      )}
    >
      {subject && (
        <div className="flex items-center gap-2 border-b border-[var(--color-border)] px-3 pb-2 pt-1.5 text-xs text-[var(--color-fg-subtle)]">
          <span className="min-w-0 flex-1 truncate">
            Signed in as <span className="text-[var(--color-fg-muted)]">{privacy ? name : subject}</span>
          </span>
          {privacy && (
            <span
              data-privacy-badge
              title="Email addresses are hidden in this browser"
              className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] font-medium text-[var(--color-fg-muted)]"
            >
              Privacy mode
            </span>
          )}
        </div>
      )}
      <MenuGroup label="Work">
        {item("library", "Library", BooksIcon)}
        {/* Team dashboard + borrow billing: every member sees their own
            agents; org admins see the whole team (gateway gates /v1/tenant/*). */}
        {item("team", "Team", UsersThreeIcon)}
      </MenuGroup>
      <MenuGroup label="Workspace">
        {item("components", "Components", PackageIcon)}
        {/* Organization/Platform console: org_admin manages their own tenant,
            super_admin gets the cross-tenant variant. Members never see it and
            the gateway enforces the permission on the underlying APIs. */}
        {isAdmin && item("org", can(role, "manage_platform") ? "Platform" : "Organization", BuildingsIcon)}
        {item("directory", "Organizations", GlobeIcon)}
      </MenuGroup>
      <MenuGroup label="Me">
        {item("settings", "Settings", GearSixIcon)}
        {item("releases", "Download desktop app", DownloadSimpleIcon)}
        <MenuItem icon={SignOutIcon} onSelect={onSignOut}>
          Sign out
        </MenuItem>
      </MenuGroup>
      <div className="border-t border-[var(--color-border)] px-3 pb-0.5 pt-1.5">
        <BuildVersion />
      </div>
    </Menu>
  );
}
