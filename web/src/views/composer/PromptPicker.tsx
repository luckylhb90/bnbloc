import { useEffect, useMemo, useRef, useState } from "react";
import { ChatTextIcon, MagnifyingGlassIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import {
  fillPrompt,
  initialValues,
  missingValue,
  promptMatches,
  variableLabel,
  type PromptChoice,
} from "@/lib/promptPacks";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";

const optionRow = (active: boolean) =>
  cn(
    "flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm",
    active
      ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
      : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
  );

// SlashMenu lists the prompts matching a typed "/command" above the input.
export function SlashMenu({
  matches,
  active,
  listId,
  onPick,
  onHover,
}: {
  readonly matches: readonly PromptChoice[];
  readonly active: number;
  readonly listId: string;
  readonly onPick: (i: number) => void;
  readonly onHover: (i: number) => void;
}) {
  if (matches.length === 0) return null;
  return (
    <div className="absolute bottom-full left-0 z-20 mb-2 w-80 overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
      <div className="border-b border-[var(--color-border)] px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
        Prompts
      </div>
      <div id={listId} role="listbox" aria-label="Prompt commands">
        {matches.map((c, i) => (
          <div
            key={`${c.componentId}:${c.prompt.id}`}
            id={`${listId}-${i}`}
            role="option"
            aria-selected={i === active}
            onMouseDown={(e) => e.preventDefault()}
            onMouseEnter={() => onHover(i)}
            onClick={() => onPick(i)}
            className={optionRow(i === active)}
          >
            <span className="shrink-0 font-mono text-xs text-[var(--color-accent)]">{c.prompt.slash}</span>
            <span className="min-w-0 flex-1 truncate">{c.prompt.title}</span>
            <span className="max-w-[7rem] shrink-0 truncate text-xs text-[var(--color-fg-subtle)]">{c.packLabel}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// PromptPickerButton is the composer's prompt picker: a searchable list of
// the prompts of the member's packs and the room's.
export function PromptPickerButton({
  choices,
  onPick,
  disabled,
  className,
}: {
  readonly choices: readonly PromptChoice[];
  readonly onPick: (c: PromptChoice) => void;
  readonly disabled?: boolean;
  readonly className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);
  const shown = useMemo(() => promptMatches(choices, q), [choices, q]);
  const pick = (c: PromptChoice) => {
    setOpen(false);
    setQ("");
    onPick(c);
  };
  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        disabled={disabled}
        className={className}
        title="Insert a prompt"
        aria-label="Insert a prompt"
        aria-haspopup="dialog"
        aria-expanded={open}
      >
        <ChatTextIcon size={18} />
      </button>
      {open && (
        <div className="animate-in absolute bottom-full left-0 z-20 mb-2 flex max-h-80 w-80 flex-col overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
          <div className="relative border-b border-[var(--color-border)] p-2">
            <MagnifyingGlassIcon size={14} className="pointer-events-none absolute left-4 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
            <Input autoFocus className="h-8 pl-7 text-sm" placeholder="Search prompts…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search prompts" />
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto py-1">
            {choices.length === 0 ? (
              <p className="px-3 py-3 text-xs text-[var(--color-fg-subtle)]">
                No prompts yet. Save a message as a prompt, or turn on a prompt pack in the Library.
              </p>
            ) : shown.length === 0 ? (
              <p className="px-3 py-3 text-xs text-[var(--color-fg-subtle)]">Nothing matches.</p>
            ) : (
              shown.map((c) => (
                <button key={`${c.componentId}:${c.prompt.id}`} type="button" onClick={() => pick(c)} className={cn(optionRow(false), "flex-col items-start gap-0.5")}>
                  <span className="flex w-full min-w-0 items-center gap-2">
                    <span className="min-w-0 flex-1 truncate font-medium text-[var(--color-fg)]">{c.prompt.title}</span>
                    {c.prompt.slash && <span className="shrink-0 font-mono text-xs text-[var(--color-accent)]">{c.prompt.slash}</span>}
                  </span>
                  <span className="line-clamp-1 w-full text-xs">{c.prompt.body}</span>
                  <span className="text-[0.65rem] text-[var(--color-fg-subtle)]">
                    {c.packLabel}
                    {c.source === "room" ? " · this room" : ""}
                    {c.prompt.uses > 0 ? ` · used ${c.prompt.uses}×` : ""}
                  </span>
                </button>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// PromptVariablesDialog asks for a prompt's variables (a select where the
// variable lists options) and shows the prompt as it will be inserted.
export function PromptVariablesDialog({
  choice,
  onCancel,
  onInsert,
}: {
  readonly choice: PromptChoice | null;
  readonly onCancel: () => void;
  readonly onInsert: (values: Readonly<Record<string, string>>) => void;
}) {
  const vars = choice?.prompt.variables ?? [];
  const [values, setValues] = useState<Record<string, string>>({});
  useEffect(() => {
    if (choice) setValues(initialValues(choice.prompt.variables));
  }, [choice]);
  const missing = missingValue(vars, values);
  const submit = () => {
    if (!missing) onInsert(values);
  };
  return (
    <Dialog
      open={!!choice}
      onClose={onCancel}
      size="md"
      title={choice?.prompt.title}
      description={choice ? `From ${choice.packLabel}` : undefined}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={!!missing} title={missing ? `Fill in ${variableLabel(missing)}` : undefined}>
            Insert
          </Button>
        </>
      }
    >
      {choice && (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          {vars.map((v, i) => (
            <Field key={v.name} label={variableLabel(v)}>
              {v.options && v.options.length > 0 ? (
                <Select value={values[v.name] ?? ""} onChange={(e) => setValues((cur) => ({ ...cur, [v.name]: e.target.value }))}>
                  {v.options.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </Select>
              ) : (
                <Input autoFocus={i === 0} value={values[v.name] ?? ""} onChange={(e) => setValues((cur) => ({ ...cur, [v.name]: e.target.value }))} />
              )}
            </Field>
          ))}
          <div>
            <p className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Preview</p>
            <p className="max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-sm text-[var(--color-fg-muted)]">
              {fillPrompt(choice.prompt.body, values)}
            </p>
          </div>
          <button type="submit" className="hidden" aria-hidden="true" tabIndex={-1} />
        </form>
      )}
    </Dialog>
  );
}
