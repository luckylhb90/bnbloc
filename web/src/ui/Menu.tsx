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
import { cn } from "@/lib/cn";
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

const ITEM = '[role="menuitem"]:not([disabled])';
// The gap kept between an open surface and the edge that clips it.
const EDGE = 4;

/** The box that clips the menu: the nearest ancestor that does not let
 *  content overflow horizontally (a dock panel), else the window. */
function clippingBox(from: HTMLElement | null): { left: number; right: number } {
  for (let el = from; el; el = el.parentElement) {
    if (getComputedStyle(el).overflowX !== "visible") {
      const r = el.getBoundingClientRect();
      return { left: r.left, right: r.right };
    }
  }
  return { left: 0, right: window.innerWidth };
}

/** Keeps an open surface inside the box that clips it, whichever side its
 *  position class anchors it to: it flips to the other edge of its trigger's
 *  box when it overflows, and is narrowed when it fits on neither. */
function keepInside(el: HTMLElement): void {
  el.style.left = "";
  el.style.right = "";
  el.style.maxWidth = "";
  const box = clippingBox(el.parentElement);
  let r = el.getBoundingClientRect();
  if (r.left < box.left + EDGE) {
    el.style.left = "0";
    el.style.right = "auto";
  } else if (r.right > box.right - EDGE) {
    el.style.right = "0";
    el.style.left = "auto";
  } else return;
  r = el.getBoundingClientRect();
  if (r.left < box.left + EDGE || r.right > box.right - EDGE) el.style.maxWidth = `${Math.max(120, box.right - box.left - 2 * EDGE)}px`;
  r = el.getBoundingClientRect();
  // Still past an edge (a narrow box): slide along it.
  if (r.left < box.left + EDGE) el.style.transform = `translateX(${box.left + EDGE - r.left}px)`;
  else if (r.right > box.right - EDGE) el.style.transform = `translateX(${box.right - EDGE - r.right}px)`;
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

  useLayoutEffect(() => {
    if (open && menuRef.current) keepInside(menuRef.current);
  }, [open]);

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
      {open && (
        <CloseMenu.Provider value={close}>
          <div
            ref={menuRef}
            id={menuId}
            role="menu"
            aria-label={label}
            tabIndex={-1}
            onKeyDown={onMenuKey}
            className={cn(
              "animate-in z-50 flex flex-col overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] py-1 shadow-[var(--shadow-pop)] focus:outline-none",
              menuClassName ?? "absolute bottom-full left-0 mb-2 w-60",
            )}
          >
            {children}
          </div>
        </CloseMenu.Provider>
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
