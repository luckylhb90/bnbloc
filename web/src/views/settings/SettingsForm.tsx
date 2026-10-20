import { useEffect, useState, type ReactNode } from "react";
import {
  initialValues,
  isSecret,
  missingRequired,
  needsSetupText,
  scopeText,
  settingInput,
  settingsUpdate,
  shownValue,
  type ComponentSetting,
  type ComponentSettingField,
  type ComponentSettingsUpdate,
} from "@/lib/componentSettings";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { Field, Input } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// SettingsForm is the one form for a component's settings
// (docs/prd/2026-10-19-component-settings.md), wherever their scope is set:
// the platform console (platform scope), Organization → Component settings
// (org scope) and the Components page (member scope, on the desktop).
// `fields` are the settings this form edits; `others` are settings of other
// scopes, shown read-only as set or not (never a value). A secret is
// write-only: left empty it keeps the stored one, and a stored optional
// secret can be removed on save. The server (or the desktop shell) checks
// every value against the declaration; its refusal is shown as is.
export function SettingsForm({
  title,
  version,
  status,
  fields,
  others = [],
  missing = [],
  declaration,
  reachesMembers = false,
  asks = false,
  onSave,
  savedText,
  children,
}: {
  readonly title: string;
  readonly version?: string;
  readonly status?: string;
  readonly fields: readonly ComponentSettingField[];
  readonly others?: readonly ComponentSettingField[];
  // Keys of the required settings not set yet, any scope (needs setup).
  readonly missing?: readonly string[];
  // The settings `missing` names, when not all are among the fields.
  readonly declaration?: readonly ComponentSetting[];
  // The component runs on members' computers: these values go there.
  readonly reachesMembers?: boolean;
  // An endpoint component beside the gateway reads these values itself.
  readonly asks?: boolean;
  readonly onSave?: (update: ComponentSettingsUpdate) => Promise<void>;
  readonly savedText?: string;
  readonly children?: ReactNode;
}) {
  const toast = useToast();
  const [values, setValues] = useState<Record<string, string>>(() => initialValues(fields));
  const [clear, setClear] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  // The fields as stored: a new answer (a save, a reload) resets the form;
  // a re-render with the same answer keeps what is being typed.
  const stored = JSON.stringify(fields);
  useEffect(() => {
    setValues(initialValues(JSON.parse(stored) as ComponentSettingField[]));
    setClear([]);
  }, [stored]);

  const save = async () => {
    if (!onSave) return;
    const missingNow = missingRequired(fields, values);
    if (missingNow.length > 0) {
      toast.error(`Fill in ${missingNow.join(", ")}`);
      return;
    }
    setBusy(true);
    try {
      await onSave(settingsUpdate(fields, values, clear));
      if (savedText) toast.success(savedText);
    } catch (e) {
      toast.error(errorText(e, "Could not save the settings"));
    } finally {
      setBusy(false);
    }
  };

  const setup = needsSetupText(missing, declaration ?? [...fields, ...others]);
  const set = (key: string, v: string) => setValues((cur) => ({ ...cur, [key]: v }));

  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="min-w-0">
        <span className="block text-sm font-medium">
          {title} {version && <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{version}</span>}
        </span>
        {status && <span className="block text-xs text-[var(--color-fg-muted)]">{status}</span>}
        {setup && <span className="block text-xs text-[var(--color-warning)]">{setup}</span>}
      </div>
      {children}
      {fields.length > 0 && reachesMembers && (
        <p className="text-xs text-[var(--color-fg-subtle)]">These values are sent to the computers where this component runs.</p>
      )}
      {fields.length > 0 && asks && (
        <p className="text-xs text-[var(--color-fg-subtle)]">The component reads these values from the gateway itself: a change reaches it the next time it asks, without a reinstall.</p>
      )}
      {fields.length > 0 && (
        <div className="flex flex-col gap-3">
          {fields.map((f) => (
            <SettingInput
              key={f.key}
              field={f}
              value={values[f.key] ?? ""}
              cleared={clear.includes(f.key)}
              onChange={(v) => set(f.key, v)}
              onClear={(on) => setClear((cur) => (on ? [...cur, f.key] : cur.filter((k) => k !== f.key)))}
            />
          ))}
        </div>
      )}
      {others.length > 0 && (
        <ul className="flex flex-col gap-1 text-xs text-[var(--color-fg-muted)]">
          {others.map((f) => (
            <li key={f.key}>
              <span className="font-medium text-[var(--color-fg)]">{f.label}</span>: {scopeText(f.scope)} · {f.set ? "set" : f.default ? `default ${f.default}` : "not set"}
            </li>
          ))}
        </ul>
      )}
      {fields.length > 0 && onSave && (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="primary" disabled={busy} onClick={() => void save()}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </div>
      )}
    </Card>
  );
}

// SettingInput is one editable setting: the control its type renders as,
// its scope, and for a stored optional secret a way to remove it on save.
function SettingInput({
  field: f,
  value,
  cleared,
  onChange,
  onClear,
}: {
  readonly field: ComponentSettingField;
  readonly value: string;
  readonly cleared: boolean;
  readonly onChange: (v: string) => void;
  readonly onClear: (on: boolean) => void;
}) {
  const input = settingInput(f.type);
  const hint = [f.help, scopeText(f.scope)].filter(Boolean).join(" · ");
  const label = f.required ? `${f.label} *` : f.label;
  if (input.control === "switch") {
    return (
      <Field label={label} hint={hint}>
        <Switch checked={shownValue(f, value) === "true"} onChange={(on) => onChange(on ? "true" : "false")} ariaLabel={f.label} />
      </Field>
    );
  }
  if (input.control === "select") {
    return (
      <Field label={label} hint={hint}>
        <Select value={value} onChange={(e) => onChange(e.target.value)} className="h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm">
          <option value="">{f.default ? `Default (${f.default})` : "Not set"}</option>
          {(f.options ?? []).map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </Select>
      </Field>
    );
  }
  const secret = isSecret(f);
  return (
    <Field label={label} hint={hint}>
      <Input
        type={input.type}
        autoComplete={secret ? "new-password" : "off"}
        spellCheck={false}
        value={cleared ? "" : value}
        disabled={cleared}
        placeholder={secret && f.set && !cleared ? "set — leave empty to keep" : cleared ? "removed on save" : (f.default ?? "")}
        onChange={(e) => onChange(e.target.value)}
      />
      {secret && f.set && !f.required && (
        <span>
          <Button size="sm" variant="ghost" onClick={() => onClear(!cleared)}>
            {cleared ? "Keep the stored value" : "Remove the stored value"}
          </Button>
        </span>
      )}
    </Field>
  );
}
