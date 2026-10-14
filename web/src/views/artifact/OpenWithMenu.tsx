import { ArrowCounterClockwiseIcon, CaretDownIcon, CubeIcon, EyeIcon } from "@phosphor-icons/react";
import { Menu, MenuGroup, MenuItem } from "@/ui/Menu";
import { cn } from "@/lib/cn";
import { rememberViewer } from "@/lib/viewerPreference";
import { selectViewers, type ArtifactFileInfo } from "./registry";
import { useViewerRegistry } from "./registry";

// "Open with…" for a workbench tab: every viewer that claims the file —
// built-ins and installed previewer components from the one registry — best
// match first. Choosing one switches this tab and remembers the choice for
// files of the same type (lib/viewerPreference); "Default viewer" forgets it.

export function OpenWithMenu({
  file,
  currentViewerId,
  onChoose,
  className,
}: {
  readonly file: ArtifactFileInfo;
  /** The viewer the tab renders with now. */
  readonly currentViewerId: string | undefined;
  /** The chosen viewer id, or null for the registry's best match. */
  readonly onChoose: (viewerId: string | null) => void;
  readonly className?: string;
}) {
  useViewerRegistry();
  const candidates = selectViewers(file);
  if (candidates.length < 2) return null;
  const choose = (viewerId: string | null) => {
    rememberViewer(file, viewerId);
    onChoose(viewerId);
  };
  return (
    <Menu
      label="Open with"
      className={className}
      menuClassName="absolute right-0 top-full mt-1 w-64"
      trigger={(props, open) => (
        <button
          {...props}
          title="Open with…"
          className={cn(
            "tap tactile inline-flex h-8 items-center gap-1 rounded-[var(--radius)] px-2 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
            open && "bg-[var(--color-surface-2)] text-[var(--color-fg)]",
          )}
        >
          Open with <CaretDownIcon size={12} aria-hidden />
        </button>
      )}
    >
      <MenuGroup label={`Open ${file.name} with`}>
        {candidates.map(({ viewer }) => (
          <MenuItem
            key={viewer.id}
            icon={viewer.source === "component" ? CubeIcon : EyeIcon}
            current={viewer.id === currentViewerId}
            onSelect={() => choose(viewer.id)}
          >
            {viewer.label}
            {viewer.source === "component" ? " (component)" : ""}
          </MenuItem>
        ))}
      </MenuGroup>
      <MenuGroup label="Default">
        <MenuItem icon={ArrowCounterClockwiseIcon} onSelect={() => choose(null)}>
          Default viewer for this file type
        </MenuItem>
      </MenuGroup>
    </Menu>
  );
}
