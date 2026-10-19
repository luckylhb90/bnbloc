import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { ComponentSettingScopeValues, ComponentSettingTypeValues } from "@/lib/wire.gen";
import { scopeText } from "@/lib/componentSettings";
import type { McpForm, McpFormSetting } from "@/lib/mcpPackageForm";
import { Button } from "@/ui/Button";
import { Input } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";

const selectClass = "h-10 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm";

// McpSettingsEditor is the MCP tool form's settings
// (docs/prd/2026-10-19-component-settings.md): each value the tool needs,
// who sets it, and the env var (stdio) or request header (http) it reaches
// the server in. Values never go in the package; the gateway validates the
// declaration when it assembles the package.
export function McpSettingsEditor({ form, onChange }: { readonly form: McpForm; readonly onChange: (settings: readonly McpFormSetting[]) => void }) {
  const set = (i: number, patch: Partial<McpFormSetting>) => onChange(form.settings.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const stdio = form.runtime === "stdio";
  return (
    <div className="grid gap-2">
      <p className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Settings</p>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        What the server needs set, and who sets it. {stdio ? "A stdio server reads each from its env var (or its settings file)." : "An http server gets each in a request header (the credential's goes in the authentication below)."}
      </p>
      {form.settings.map((x, i) => (
        <div key={i} className="grid gap-2 rounded-[var(--radius)] border border-[var(--color-border)] p-2">
          <div className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)_auto] gap-2">
            <Input aria-label="Setting key" value={x.key} onChange={(e) => set(i, { key: e.target.value })} placeholder="apiKey" className="font-mono" />
            <Input aria-label="Setting label" value={x.label} onChange={(e) => set(i, { label: e.target.value })} placeholder="API key" />
            <Button variant="ghost" aria-label="Remove setting" onClick={() => onChange(form.settings.filter((_, j) => j !== i))}>
              <TrashIcon size={15} />
            </Button>
          </div>
          <div className="grid grid-cols-[auto_auto_minmax(0,1fr)_auto] items-center gap-2">
            <Select aria-label="Setting type" value={x.type} onChange={(e) => set(i, { type: e.target.value as McpFormSetting["type"] })} className={selectClass}>
              {/* A choice needs its options: write that package by hand. */}
              {ComponentSettingTypeValues.filter((t) => t !== "choice").map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </Select>
            <Select aria-label="Who sets it" value={x.scope} onChange={(e) => set(i, { scope: e.target.value as McpFormSetting["scope"] })} className={selectClass}>
              {ComponentSettingScopeValues.map((s) => (
                <option key={s} value={s}>
                  {scopeText(s)}
                </option>
              ))}
            </Select>
            <Input
              aria-label={stdio ? "Env var" : "Header"}
              value={x.deliver}
              onChange={(e) => set(i, { deliver: stdio ? e.target.value.toUpperCase() : e.target.value })}
              placeholder={stdio ? "ENV_VAR (optional)" : "X-Header (optional)"}
              className="font-mono"
            />
            <Switch checked={x.required} onChange={(required) => set(i, { required })} label="Required" />
          </div>
          <Input aria-label="Setting help" value={x.help} onChange={(e) => set(i, { help: e.target.value })} placeholder="Help shown under the field (optional)" />
        </div>
      ))}
      <div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => onChange([...form.settings, { key: "", label: "", type: "secret", scope: "org", required: true, help: "", deliver: "" }])}
        >
          <PlusIcon size={14} /> Add a setting
        </Button>
      </div>
    </div>
  );
}
