import {
  createContext,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "@/lib/cn";
import { placeSurface, preferredPlacement, sizeClasses } from "@/ui/menuPlacement";
import { useModalLayer } from "@/ui/useModalLayer";

// Menu is the shared menu-button primitive (WAI-ARIA menu button pattern):
//   - the trigger carries aria-haspopup="menu" / aria-expanded / aria-controls;
//     Enter/Space/click toggle it, ArrowDown opens on the first item and
//     ArrowUp on the last;
//   - inside, ArrowUp/ArrowDown/Home/End move between items, Tab closes;
//   - Escape closes through the modal layer stack (so a menu opened inside the
//     mobile drawer closes alone, not together with the drawer), and focus
//     returns to the trigger;
//   - a pointer press outside closes it.
// The item marked `current` takes initial focus, so reopening a menu lands on
// where the user already is.

export interface MenuTriggerProps {
  readonly ref: RefObject<HTMLButtonElement | null>;
  readonly type: "button";
  readonly "aria-haspopup": "menu";
  readonly "aria-expanded": boolean;
  readonly "aria-controls": string | undefined;
  readonly onClick: () => void;
  readonly onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => void;
}

const CloseMenu = createContext<() => void>(() => {});

const ITEM = '[role="menuitem"]:not([disabled]), [role="menuitemradio"]:not([disabled])';
function place(surface: HTMLElement, trigger: HTMLElement, cls: string): void {
  surface.style.maxHeight = "";
  surface.style.maxWidth = "";
  const p = placeSurface(trigger.getBoundingClientRect(), { width: surface.offsetWidth, height: surface.scrollHeight }, { width: window.innerWidth, height: window.innerHeight }, preferredPlacement(cls));
  surface.style.left = `${p.left}px`;
  surface.style.top = `${p.top}px`;
  surface.style.maxWidth = `${p.maxWidth}px`;
  surface.style.maxHeight = `${p.maxHeight}px`;
}

export function Menu({
  label,
  trigger,
  children,
  className,
  menuClassName,
}: {
  // Accessible name of the menu surface.
  readonly label: string;
  readonly trigger: (props: MenuTriggerProps, open: boolean) => ReactNode;
  readonly children: ReactNode;
  readonly className?: string;
  // Positions the surface; defaults to anchored bottom-left, opening upward.
  readonly menuClassName?: string;
}) {
  const [open, setOpen] = useState<false | "first" | "last">(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const menuId = useId();
  const close = () => setOpen(false);

  // Declared before the focus effect below: the layer records the trigger as
  // the element to restore focus to before the menu moves focus inside.
  useModalLayer(menuRef, { open: open !== false, onEscape: close, lockScroll: false });

  const positionClass = menuClassName ?? "absolute bottom-full left-0 mb-2 w-60";
  useLayoutEffect(() => {
    if (!open) return;
    const reposition = () => {
      if (menuRef.current && triggerRef.current) place(menuRef.current, triggerRef.current, positionClass);
    };
    reposition();
    // The trigger moves with any scroll or resize; the surface follows it.
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open, positionClass]);

  useEffect(() => {
    if (!open) return;
    const items = menuItems(menuRef.current);
    const current = items.find((el) => el.getAttribute("aria-current") === "page");
    (current ?? (open === "last" ? items[items.length - 1] : items[0]))?.focus();
    const onPointer = (e: PointerEvent) => {
      const t = e.target as Node;
      if (menuRef.current?.contains(t) || triggerRef.current?.contains(t)) return;
      setOpen(false);
    };
    document.addEventListener("pointerdown", onPointer, true);
    return () => document.removeEventListener("pointerdown", onPointer, true);
  }, [open]);

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const items = menuItems(menuRef.current);
    if (items.length === 0) return;
    const at = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (e.key === "ArrowDown") next = (at + 1) % items.length;
    else if (e.key === "ArrowUp") next = (at - 1 + items.length) % items.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = items.length - 1;
    else if (e.key === "Tab") {
      e.preventDefault();
      setOpen(false);
      return;
    }
    if (next === -1) return;
    e.preventDefault();
    items[next].focus();
  };

  const triggerProps: MenuTriggerProps = {
    ref: triggerRef,
    type: "button",
    "aria-haspopup": "menu",
    "aria-expanded": open !== false,
    "aria-controls": open ? menuId : undefined,
    onClick: () => setOpen((o) => (o ? false : "first")),
    onKeyDown: (e) => {
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      e.preventDefault();
      setOpen(e.key === "ArrowDown" ? "first" : "last");
    },
  };

  return (
    <div className={cn("relative", className)}>
      {trigger(triggerProps, open !== false)}
      {open &&
        createPortal(
          <CloseMenu.Provider value={close}>
            <div
              ref={menuRef}
              id={menuId}
              role="menu"
              aria-label={label}
              tabIndex={-1}
              onKeyDown={onMenuKey}
              style={{ position: "fixed", left: 0, top: 0 }}
              className={cn(
                "animate-in z-[60] flex flex-col overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] py-1 shadow-[var(--shadow-pop)] focus:outline-none",
                sizeClasses(positionClass),
              )}
            >
              {children}
            </div>
          </CloseMenu.Provider>,
          document.body,
        )}
    </div>
  );
}

function menuItems(root: HTMLElement | null): HTMLElement[] {
  return root ? Array.from(root.querySelectorAll<HTMLElement>(ITEM)) : [];
}

// MenuGroup is a labelled run of items; groups after the first get a rule.
export function MenuGroup({ label, children }: { readonly label: string; readonly children: ReactNode }) {
  const id = useId();
  return (
    <div role="group" aria-labelledby={id} className="py-1 [&+&]:border-t [&+&]:border-[var(--color-border)]">
      <div id={id} className="px-3 pb-1 pt-1.5 text-[0.65rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
        {label}
      </div>
      {children}
    </div>
  );
}

// MenuItem closes the menu (focus returns to the trigger) and then runs
// onSelect. `current` marks the entry for the view the user is on.
export function MenuItem({
  icon: Icon,
  current = false,
  onSelect,
  children,
}: {
  readonly icon?: React.ComponentType<{ size?: number; weight?: "regular" | "fill"; className?: string }>;
  readonly current?: boolean;
  readonly onSelect: () => void;
  readonly children: ReactNode;
}) {
  const close = useContext(CloseMenu);
  return (
    <button
      type="button"
      role="menuitem"
      tabIndex={-1}
      aria-current={current ? "page" : undefined}
      onClick={() => {
        close();
        onSelect();
      }}
      className={cn(
        "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm focus:outline-none",
        "hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus:bg-[var(--color-surface-2)] focus:text-[var(--color-fg)]",
        current ? "font-medium text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]",
      )}
    >
      {Icon && (
        <Icon
          size={16}
          weight={current ? "fill" : "regular"}
          className={cn("shrink-0", current && "text-[var(--color-accent)]")}
        />
      )}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {current && <span className="size-1.5 shrink-0 rounded-full bg-[var(--color-accent)]" />}
    </button>
  );
}

// MenuChoices is one of a few choices as a compact segmented row (a
// member's notification level): picking one closes the menu and runs
// onSelect; the caption says what the current one means.
export function MenuChoices<T extends string>({
  choices,
  value,
  onSelect,
  caption,
}: {
  readonly choices: readonly { readonly value: T; readonly short: string; readonly label: string }[];
  readonly value: T;
  readonly onSelect: (value: T) => void;
  readonly caption?: string;
}) {
  const close = useContext(CloseMenu);
  return (
    <div className="px-3 pb-1.5 pt-0.5">
      <div className="flex rounded-[var(--radius)] border border-[var(--color-border)] p-0.5">
        {choices.map((c) => (
          <button
            key={c.value || "default"}
            type="button"
            role="menuitemradio"
            aria-checked={c.value === value}
            aria-label={c.label}
            title={c.label}
            tabIndex={-1}
            onClick={() => {
              close();
              if (c.value !== value) onSelect(c.value);
            }}
            className={cn(
              "min-w-0 flex-1 truncate rounded-[calc(var(--radius)-2px)] px-0.5 py-1 text-[11px] focus:outline-none",
              "hover:text-[var(--color-fg)] focus:bg-[var(--color-surface-2)]",
              c.value === value ? "bg-[var(--color-surface-2)] font-semibold text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]",
            )}
          >
            {c.short}
          </button>
        ))}
      </div>
      {caption && <div className="mt-1 truncate text-[11px] text-[var(--color-fg-subtle)]">{caption}</div>}
    </div>
  );
}
