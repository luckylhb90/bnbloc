import { useEffect, useState } from "react";
import { PlugsConnectedIcon } from "@phosphor-icons/react";
import { componentPagesApi, openComponentPage } from "@/lib/componentPagesApi";
import { errorText } from "@/lib/errors";
import type { ComponentPageInfo } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Menu, MenuItem } from "@/ui/Menu";
import { useToast } from "@/ui/Toast";

// RoomServicePages offers, in a room's header, the organization's service
// pages meant for a room (ComponentPageInfo.inRoom): each opens apart from
// the app, for this room (the service is told the room and the member's
// role there). Nothing shows when there are none; one opens at a click.
export function RoomServicePages({ roomId }: { readonly roomId: string }) {
  const [pages, setPages] = useState<readonly ComponentPageInfo[]>([]);
  const toast = useToast();
  useEffect(() => {
    let live = true;
    componentPagesApi
      .list()
      .then((r) => live && setPages((r.pages ?? []).filter((p) => p.inRoom)))
      .catch(() => live && setPages([]));
    return () => {
      live = false;
    };
  }, []);
  if (pages.length === 0) return null;
  const open = (p: ComponentPageInfo) =>
    void openComponentPage(p.component, p.name, undefined, roomId).catch((e) => toast.error(errorText(e, `Could not open ${p.label}`)));
  if (pages.length === 1) {
    const p = pages[0]!;
    return (
      <Button variant="ghost" size="icon" title={`${p.label} in this room`} aria-label={`${p.label} in this room`} onClick={() => open(p)} className="tap">
        <PlugsConnectedIcon size={18} />
      </Button>
    );
  }
  return (
    <Menu
      label="Services in this room"
      className="relative"
      menuClassName="absolute right-0 top-full mt-1 w-56"
      trigger={(props) => (
        <Button {...props} variant="ghost" size="icon" title="Services in this room" aria-label="Services in this room" className="tap">
          <PlugsConnectedIcon size={18} />
        </Button>
      )}
    >
      {pages.map((p) => (
        <MenuItem key={`${p.component}/${p.name}`} onSelect={() => open(p)}>
          {p.label}
        </MenuItem>
      ))}
    </Menu>
  );
}
