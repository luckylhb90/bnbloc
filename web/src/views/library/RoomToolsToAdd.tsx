import { useState } from "react";
import { api, type MemberComponent } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { ComponentSettingsDialog } from "@/views/settings/ComponentSettingsDialog";

// RoomToolsToAdd is the install dialog's "Tools to add"
// (docs/prd/2026-10-20-complete-room-capture.md §2.2): the MCP tools a room
// component requires that the installer can add and has not. Add is the
// member catalog's install (the never-a-dead-end install action), followed
// by the tool's settings when it needs any; the result stays on its row.
export function RoomToolsToAdd({ tools }: { readonly tools: readonly string[] }) {
  if (tools.length === 0) return null;
  return (
    <section aria-label="Tools to add" className="flex flex-col gap-1">
      <p>The room&apos;s agents used these tools. Add them so they have them here too:</p>
      {tools.map((name) => (
        <ToolRow key={name} name={name} />
      ))}
    </section>
  );
}

function ToolRow({ name }: { readonly name: string }) {
  const toast = useToast();
  const [state, setState] = useState<"idle" | "busy" | "added">("idle");
  const [setup, setSetup] = useState<MemberComponent | null>(null);
  const add = async () => {
    setState("busy");
    try {
      const listed = ((await api.components()).components ?? []).find((c) => c.kind === "mcp" && c.name === name);
      if (!listed) throw new Error(`${name} is no longer listed`);
      await api.installComponent(listed);
      const added = ((await api.components()).components ?? []).find((c) => c.kind === "mcp" && c.name === name);
      setState("added");
      if (added && (added.settingsMissing ?? []).length > 0) setSetup(added);
    } catch (e) {
      toast.error(errorText(e, `Could not add ${name}`));
      setState("idle");
    }
  };
  return (
    <div className="flex items-center gap-2 text-sm">
      <span className="min-w-0 flex-1 break-words font-medium">{name}</span>
      {state === "added" ? (
        <span role="status" className="text-xs text-[var(--color-fg-muted)]">
          Added{setup ? ": set it up" : ""}
        </span>
      ) : (
        <Button size="sm" variant="outline" disabled={state === "busy"} onClick={() => void add()}>
          Add
        </Button>
      )}
      <ComponentSettingsDialog target={setup} onClose={() => setSetup(null)} />
    </div>
  );
}
