import { useEffect, useState, type ReactNode } from "react";
import { initialValues, missingRequired, settingsUpdate, type LocalSettingView } from "@/lib/pluginSettings";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// LocalSettingsCard is the one form for a component's credential fields: a
// plugin's local settings (Settings → Notifications), an MCP tool's member
// credentials (Settings → Tools) and an organization's MCP connector
// (Organization → Tool connectors). Secret values are write-only: an empty
// secret keeps the stored one.
export function LocalSettingsCard({
  title,
  version,
  status,
  fields,
  onSave,
  onClear,
  savedText,
  clearedText,
  clearLabel = "Remove from this computer",
  children,
}: {
  readonly title: string;
  readonly version?: string;
  readonly status: string;
  readonly fields: readonly LocalSettingView[];
  readonly onSave: (values: Record<string, string>) => Promise<void>;
  readonly onClear?: () => Promise<void>;
  readonly savedText: string;
  readonly clearedText: string;
  readonly clearLabel?: string;
  readonly children?: ReactNode;
}) {
  const toast = useToast();
  const [values, setValues] = useState<Record<string, string>>(() => initialValues(fields));
  const [busy, setBusy] = useState<"save" | "clear" | null>(null);

  useEffect(() => {
    setValues(initialValues(fields));
  }, [fields]);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<void>) => {
    setBusy(kind);
    try {
      await fn();
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
    } finally {
      setBusy(null);
    }
  };

  const save = () =>
    run("save", async () => {
      const missing = missingRequired(fields, values);
      if (missing.length > 0) {
        toast.error(`Fill in ${missing.join(", ")}`);
        return;
      }
      await onSave(settingsUpdate(fields, values));
      toast.success(savedText);
    });

  const clear = () =>
    onClear &&
    run("clear", async () => {
      await onClear();
      toast.success(clearedText);
    });

  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="min-w-0">
        <span className="block text-sm font-medium">
          {title} {version && <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{version}</span>}
        </span>
        <span className="block text-xs text-[var(--color-fg-muted)]">{status}</span>
      </div>
      {children}
      {fields.length > 0 && (
        <div className="flex flex-col gap-3">
          {fields.map((f) => (
            <Field key={f.key} label={f.required ? `${f.label} *` : f.label} hint={f.help}>
              <Input
                type={f.secret ? "password" : "text"}
                autoComplete={f.secret ? "new-password" : "off"}
                spellCheck={false}
                value={values[f.key] ?? ""}
                placeholder={f.secret && f.set ? "••• set (leave empty to keep)" : ""}
                onChange={(e) => setValues((cur) => ({ ...cur, [f.key]: e.target.value }))}
              />
            </Field>
          ))}
        </div>
      )}
      {fields.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="primary" disabled={busy !== null} onClick={() => void save()}>
            {busy === "save" ? "Saving…" : "Save"}
          </Button>
          {onClear && fields.some((f) => f.set) && (
            <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void clear()}>
              {busy === "clear" ? "Removing…" : clearLabel}
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
