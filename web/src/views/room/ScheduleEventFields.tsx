import { cn } from "@/lib/cn";
import { PLATFORM_EVENTS, type RecurrenceForm } from "@/lib/schedules";
import { Field, Input } from "@/ui/Input";

// ScheduleEventFields edits an event schedule's recurrence: the platform
// changes it runs on (one or more) and, when it follows component
// publishing, the one component it is limited to. Each run's text ends with
// the change's facts (the server adds them).
export function ScheduleEventFields({
  form,
  onChange,
}: {
  readonly form: RecurrenceForm;
  readonly onChange: (next: (f: RecurrenceForm) => RecurrenceForm) => void;
}) {
  const followsComponents = form.events.includes("component.published");
  return (
    <>
      <Field label="Runs when" hint="Each run gets the change's facts: what was published, its version">
        <div className="flex flex-wrap gap-1.5">
          {PLATFORM_EVENTS.map(({ value, label }) => {
            const on = form.events.includes(value);
            return (
              <button
                key={value}
                type="button"
                aria-pressed={on}
                onClick={() => onChange((f) => ({ ...f, events: on ? f.events.filter((x) => x !== value) : [...f.events, value] }))}
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
      {followsComponents && (
        <Field label="Only this component" hint="Its name; empty runs on every component">
          <Input value={form.component} onChange={(e) => onChange((f) => ({ ...f, component: e.target.value }))} placeholder="any component" className="font-mono" />
        </Field>
      )}
    </>
  );
}
