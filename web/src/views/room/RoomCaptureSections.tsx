import { formatBytes } from "@/lib/format";
import { DEPENDENCY_LABELS, roomChecklist, type RoomDependency } from "@/lib/roomCapture";
import { carryProblem, carrySize, isOn, partKey, type RoomCarry, type RoomSaveForm } from "@/lib/roomComponents";
import { MaxRoomCarryBytes } from "@/lib/wire.gen";
import { Field, Textarea } from "@/ui/Input";

const heading = "text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";
const check = "flex items-start gap-2 rounded-[var(--radius)] px-2 py-1 text-sm hover:bg-[var(--color-surface-2)]";

// RoomCaptureSections is what "Save as component" carries beyond the
// room's setup (docs/prd/2026-10-20-complete-room-capture.md §2.1): the
// room's instructions as an editable draft ("Fill with AI" drafts them from
// the conversation), what the room's way of working depends on as the AI
// read it (a checklist: carried, or left out and why), the apps made in
// the room and its files with their sizes against the package's bound, and
// the MCP tools its runs had. Every box is the member's; nothing is saved
// until they press Save.
export function RoomCaptureSections({
  form,
  deps,
  onToggle,
  onInstructions,
}: {
  readonly form: RoomSaveForm;
  readonly deps: readonly RoomDependency[];
  readonly onToggle: (key: string, on: boolean) => void;
  readonly onInstructions: (text: string) => void;
}) {
  const rows = roomChecklist(form, deps);
  const size = carrySize(form);
  const over = carryProblem(form);
  return (
    <>
      <Field
        label="Instructions"
        hint="What the room's agent should know: its role here, the steps and formats that worked, your corrections. A room made from it gets them as its expert."
      >
        <Textarea id="room-save-instructions" value={form.instructions} onChange={(e) => onInstructions(e.target.value)} rows={5} />
      </Field>
      {rows.length > 0 && (
        <section aria-label="Depends on" className="flex flex-col gap-1">
          <span className={heading}>Depends on (AI)</span>
          {rows.map((r) => (
            <div key={r.what} className="flex items-start gap-2 px-2 py-1 text-sm">
              {r.key ? (
                <input type="checkbox" className="mt-1" aria-label={r.what} checked={r.carried} onChange={(e) => onToggle(r.key ?? "", e.target.checked)} />
              ) : (
                <span aria-hidden className="mt-0.5 w-3 text-center text-xs">{r.carried ? "✓" : "–"}</span>
              )}
              <span className="min-w-0 break-words">
                <span className="font-medium">{r.what}</span>
                <span className="text-[var(--color-fg-muted)]">
                  {" "}
                  · {DEPENDENCY_LABELS[r.kind]} · {r.carried ? "included" : "left out"}
                  {r.why ? `: ${r.why}` : ""}
                </span>
              </span>
            </div>
          ))}
        </section>
      )}
      <CarryList title="Apps made here (copied in, without their data)" kind="madeApp" items={form.madeApps} form={form} onToggle={onToggle} />
      <CarryList title="Room files" kind="file" items={form.files} form={form} onToggle={onToggle} />
      {(form.madeApps.length > 0 || form.files.length > 0) && (
        <p className={`px-2 text-xs ${over ? "text-[var(--color-danger)]" : "text-[var(--color-fg-muted)]"}`} role="status">
          {over ?? `Carried: ${formatBytes(size)} of ${formatBytes(MaxRoomCarryBytes)}`}
        </p>
      )}
      {form.tools.length > 0 && (
        <section aria-label="Tools" className="flex flex-col gap-0.5">
          <span className={heading}>Tools its runs had (whoever installs it is asked to add them)</span>
          {form.tools.map((t) => (
            <label key={t.name} className={check}>
              <input type="checkbox" className="mt-1" checked={isOn(form, partKey("tool", t.name))} onChange={(e) => onToggle(partKey("tool", t.name), e.target.checked)} />
              <span className="min-w-0 break-words">{t.name}</span>
            </label>
          ))}
        </section>
      )}
    </>
  );
}

/** CarryList lists the apps or files a package can carry, each with its
 *  size and a tick box. */
function CarryList({
  title,
  kind,
  items,
  form,
  onToggle,
}: {
  readonly title: string;
  readonly kind: "madeApp" | "file";
  readonly items: readonly RoomCarry[];
  readonly form: RoomSaveForm;
  readonly onToggle: (key: string, on: boolean) => void;
}) {
  if (items.length === 0) return null;
  return (
    <section aria-label={title} className="flex flex-col gap-0.5">
      <span className={heading}>{title}</span>
      {items.map((x) => (
        <label key={x.key} className={check}>
          <input type="checkbox" className="mt-1" checked={isOn(form, partKey(kind, x.key))} onChange={(e) => onToggle(partKey(kind, x.key), e.target.checked)} />
          <span className="min-w-0 flex-1 break-words">{x.label}</span>
          {x.sizeBytes > 0 && <span className="shrink-0 text-xs text-[var(--color-fg-muted)]">{formatBytes(x.sizeBytes)}</span>}
        </label>
      ))}
    </section>
  );
}
