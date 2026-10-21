import { cn } from "@/lib/cn";
import { EVENT_SOURCES, sourceTypes, type EventSubscriptionSource, type RecurrenceForm } from "@/lib/schedules";
import { Select } from "@/ui/Select";
import { Field, Input, Textarea } from "@/ui/Input";

const SELECT =
  "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:outline-none focus:border-[var(--color-accent)]";

// ScheduleEventFields edits an event trigger: where its events come from
// (the platform, this room, a member of it, another system through the
// task's webhook), the types it takes (picked from the closed list of the
// platform's and the room's, typed for the others) and its filter (the
// subject, exact or a prefix ending in *, and top-level data fields). Each
// run's text ends with the event as data (the server adds it).
export function ScheduleEventFields({
  form,
  onChange,
}: {
  readonly form: RecurrenceForm;
  readonly onChange: (next: (f: RecurrenceForm) => RecurrenceForm) => void;
}) {
  const closed = sourceTypes(form.source);
  const setSource = (source: EventSubscriptionSource) =>
    onChange((f) => ({ ...f, source, types: sourceTypes(source).length > 0 ? [sourceTypes(source)[0].value] : f.types }));
  return (
    <>
      <Field label="Events from">
        <Select className={SELECT} value={form.source} onChange={(e) => setSource(e.target.value as EventSubscriptionSource)}>
          {EVENT_SOURCES.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </Select>
      </Field>
      {closed.length > 0 ? (
        <Field label="Runs when" hint="Each run gets the event as data: what changed, and its details">
          <div className="flex flex-wrap gap-1.5">
            {closed.map(({ value, label }) => {
              const on = form.types.includes(value);
              return (
                <button
                  key={value}
                  type="button"
                  aria-pressed={on}
                  onClick={() => onChange((f) => ({ ...f, types: on ? f.types.filter((x) => x !== value) : [...f.types, value] }))}
                  className={cn(
                    "tactile rounded-full border px-2.5 py-0.5 text-xs",
                    on
                      ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
                      : "border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                  )}
                >
                  {label}
                </button>
              );
            })}
          </div>
        </Field>
      ) : (
        <Field
          label="Event types"
          hint={form.source === "webhook" ? "e.g. com.github.push, ci.finished; a trailing * takes a prefix" : "The types members send, e.g. dev.finished; a trailing * takes a prefix"}
        >
          <Textarea rows={2} value={form.typesText} onChange={(e) => onChange((f) => ({ ...f, typesText: e.target.value }))} className="font-mono" />
        </Field>
      )}
      <Field label="Only when the subject is" hint="Optional; end with * for a prefix (e.g. reports/*)">
        <Input value={form.subject} onChange={(e) => onChange((f) => ({ ...f, subject: e.target.value }))} placeholder="any subject" className="font-mono" />
      </Field>
      <Field label="Only when its data has" hint="Optional; field=value, one per line (top-level fields)">
        <Textarea rows={2} value={form.dataFilter} onChange={(e) => onChange((f) => ({ ...f, dataFilter: e.target.value }))} placeholder="ref=refs/heads/main" className="font-mono" />
      </Field>
    </>
  );
}
