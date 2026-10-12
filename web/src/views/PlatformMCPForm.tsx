import { useState } from "react";
import { CopySimpleIcon, DownloadSimpleIcon, PlusIcon, TrashIcon, WrenchIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { buildMCPPackage } from "@/lib/mcpPackageApi";
import {
  BINARY_PLATFORMS,
  emptyMcpForm,
  mcpDraftBody,
  mcpFormProblems,
  mcpPackageFileName,
  networkHost,
  signSteps,
  type McpForm,
  type McpFormAuth,
  type McpFormEnv,
  type McpFormTool,
} from "@/lib/mcpPackageForm";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

// PlatformMCPForm builds an MCP tool package from a form (docs/prd/2026-10-
// 12-experts-and-teams.md, "Gap closure" 10). Binaries go through the
// artifact staging the publish wizard uses; the gateway assembles the
// package and answers the JSON to sign. The console never signs: the result
// is a file plus the offline step.
export function PlatformMCPForm() {
  const [open, setOpen] = useState(false);
  return (
    <Card className="p-4">
      <div className="flex items-start gap-3">
        <div className="grid size-10 shrink-0 place-items-center rounded-[var(--radius)] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
          <WrenchIcon size={20} weight="fill" />
        </div>
        <div className="min-w-0 flex-1">
          <h3 className="text-base font-semibold text-[var(--color-fg)]">Build an MCP tool</h3>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Describe a tool server, upload its binaries or give its URL, and get the package to sign. Experts and skill packs can then require it by name.
          </p>
        </div>
        <Button variant={open ? "ghost" : "secondary"} onClick={() => setOpen((v) => !v)} aria-expanded={open}>
          {open ? "Close" : "Start"}
        </Button>
      </div>
      {open && <McpFormBody />}
    </Card>
  );
}

function McpFormBody() {
  const toast = useToast();
  const [form, setForm] = useState<McpForm>(emptyMcpForm);
  const [platform, setPlatform] = useState<string>(BINARY_PLATFORMS[0]);
  const [uploading, setUploading] = useState<string | null>(null);
  const [building, setBuilding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ readonly json: string; readonly file: string } | null>(null);
  const set = (patch: Partial<McpForm>) => {
    setForm((f) => ({ ...f, ...patch }));
    setResult(null);
  };
  const setTool = (i: number, patch: Partial<McpFormTool>) => set({ tools: form.tools.map((t, j) => (j === i ? { ...t, ...patch } : t)) });
  const setEnv = (i: number, patch: Partial<McpFormEnv>) => set({ env: form.env.map((e, j) => (j === i ? { ...e, ...patch } : e)) });
  const envNames = form.env.map((e) => e.name.trim()).filter((n) => n !== "");

  const upload = async (file: File | undefined) => {
    if (!file) return;
    setError(null);
    setUploading(`Uploading ${file.name}…`);
    try {
      const up = await api.uploadArtifact(file, { onProgress: setUploading });
      set({ binaries: [...form.binaries.filter((b) => b.platform !== platform), { platform, key: up.key, fileName: file.name }] });
    } catch (e) {
      setError(e instanceof Error ? e.message : "The upload failed");
    } finally {
      setUploading(null);
    }
  };

  const build = async () => {
    const problems = mcpFormProblems(form);
    if (problems.length > 0) {
      setError(problems.join(" "));
      return;
    }
    setError(null);
    setBuilding(true);
    try {
      const json = await buildMCPPackage(mcpDraftBody(form));
      setResult({ json, file: mcpPackageFileName(form.name, form.version) });
    } catch (e) {
      setError(e instanceof ApiError && e.message ? e.message : "Could not build the package");
    } finally {
      setBuilding(false);
    }
  };

  const download = () => {
    if (!result) return;
    const url = URL.createObjectURL(new Blob([result.json], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = result.file;
    a.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const copy = () => {
    if (!result) return;
    navigator.clipboard.writeText(result.json).then(
      () => toast.success("Package copied"),
      () => toast.error("Could not copy the package"),
    );
  };

  return (
    <div className="mt-4 grid gap-4 border-t border-[var(--color-border)] pt-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Name">
          <Input value={form.name} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="codeword" />
        </Field>
        <Field label="Version">
          <Input value={form.version} onChange={(e) => set({ version: e.target.value })} />
        </Field>
        <Field label="Publisher">
          <Input value={form.publisher} onChange={(e) => set({ publisher: e.target.value })} placeholder="publisher-id" />
        </Field>
      </div>
      <Field label="Description">
        <Input value={form.description} onChange={(e) => set({ description: e.target.value })} placeholder="What the tool server does" />
      </Field>
      <Field label="How it runs">
        <Segment
          options={[
            { value: "stdio", label: "Binary", hint: "stdio" },
            { value: "endpoint", label: "Service URL", hint: "http" },
          ]}
          value={form.runtime}
          onChange={(runtime) => set({ runtime })}
        />
      </Field>

      <div className="grid gap-2">
        <p className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Tools</p>
        {form.tools.map((t, i) => (
          <div key={i} className="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] gap-2">
            <Input aria-label="Tool name" value={t.name} onChange={(e) => setTool(i, { name: e.target.value })} placeholder="tool_name" className="font-mono" />
            <Input aria-label="Tool description" value={t.description} onChange={(e) => setTool(i, { description: e.target.value })} placeholder="What it does (optional)" />
            <Button variant="ghost" aria-label="Remove tool" onClick={() => set({ tools: form.tools.filter((_, j) => j !== i) })}>
              <TrashIcon size={15} />
            </Button>
          </div>
        ))}
        <div>
          <Button variant="ghost" size="sm" onClick={() => set({ tools: [...form.tools, { name: "", description: "" }] })}>
            <PlusIcon size={14} /> Add a tool
          </Button>
        </div>
      </div>

      <div className="grid gap-2">
        <p className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Environment</p>
        <p className="text-xs text-[var(--color-fg-subtle)]">Variables the server needs. Values never go in the package.</p>
        {form.env.map((v, i) => (
          <div key={i} className="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto_auto_auto] items-center gap-2">
            <Input aria-label="Variable name" value={v.name} onChange={(e) => setEnv(i, { name: e.target.value.toUpperCase() })} placeholder="API_TOKEN" className="font-mono" />
            <Input aria-label="Variable description" value={v.description} onChange={(e) => setEnv(i, { description: e.target.value })} placeholder="What it is (optional)" />
            <Switch checked={v.secret} onChange={(secret) => setEnv(i, { secret })} label="Secret" />
            <Switch checked={v.required} onChange={(required) => setEnv(i, { required })} label="Required" />
            <Button variant="ghost" aria-label="Remove variable" onClick={() => set({ env: form.env.filter((_, j) => j !== i) })}>
              <TrashIcon size={15} />
            </Button>
          </div>
        ))}
        <div>
          <Button variant="ghost" size="sm" onClick={() => set({ env: [...form.env, { name: "", description: "", secret: true, required: true }] })}>
            <PlusIcon size={14} /> Add a variable
          </Button>
        </div>
        {envNames.length > 0 && (
          <Field label="Who provides the values">
            <Segment
              options={[
                { value: "member", label: "Each member", hint: "on their desktop" },
                { value: "org", label: "The organization", hint: "an admin's connector" },
              ]}
              value={form.credentials}
              onChange={(credentials) => set({ credentials })}
            />
          </Field>
        )}
      </div>

      {form.runtime === "stdio" ? (
        <div className="grid gap-3">
          <Field label="Binaries" hint="One per platform. The desktop downloads the member's platform and the agent starts it per session.">
            <div className="flex flex-wrap items-center gap-2">
              <Select value={platform} onChange={(e) => setPlatform(e.target.value)} className="h-10 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm">
                {BINARY_PLATFORMS.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
              <input
                type="file"
                aria-label={`Binary for ${platform}`}
                disabled={uploading !== null}
                onChange={(e) => {
                  void upload(e.target.files?.[0]);
                  e.target.value = "";
                }}
                className="text-sm"
              />
            </div>
          </Field>
          {uploading && <p className="text-xs text-[var(--color-fg-subtle)]">{uploading}</p>}
          {form.binaries.length > 0 && (
            <ul className="grid gap-1 text-sm">
              {form.binaries.map((b) => (
                <li key={b.platform} className="flex items-center gap-2">
                  <span className="font-mono">{b.platform}</span>
                  <span className="min-w-0 flex-1 truncate text-[var(--color-fg-muted)]" title={b.key}>
                    {b.fileName} · {b.key.slice("sha256/".length, "sha256/".length + 12)}…
                  </span>
                  <Button variant="ghost" size="sm" aria-label={`Remove the ${b.platform} binary`} onClick={() => set({ binaries: form.binaries.filter((x) => x.platform !== b.platform) })}>
                    <TrashIcon size={14} />
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <Field label="Arguments (one per line)">
            <Textarea rows={2} value={form.argsText} onChange={(e) => set({ argsText: e.target.value })} placeholder="--stdio" className="font-mono text-xs" />
          </Field>
        </div>
      ) : (
        <div className="grid gap-3">
          <Field label="Server URL" hint="The MCP (streamable HTTP) endpoint. The gateway checks it answers when the package is installed.">
            <Input value={form.url} onChange={(e) => set({ url: e.target.value })} placeholder="https://tools.example.com/mcp" />
          </Field>
          <Field label="Authentication">
            <Segment
              options={[
                { value: "none", label: "None" },
                { value: "bearer", label: "Bearer token" },
                { value: "header", label: "Header" },
              ]}
              value={form.auth}
              onChange={(auth: McpFormAuth) => set({ auth })}
            />
          </Field>
          {form.auth !== "none" && (
            <div className="grid gap-3 sm:grid-cols-2">
              {form.auth === "header" && (
                <Field label="Header name">
                  <Input value={form.authHeader} onChange={(e) => set({ authHeader: e.target.value })} placeholder="X-Api-Key" />
                </Field>
              )}
              <Field label="Variable with the credential" hint={envNames.length === 0 ? "Add the variable under Environment first." : undefined}>
                <Select value={form.authEnv} onChange={(e) => set({ authEnv: e.target.value })} className="h-10 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm">
                  <option value="">Pick a variable</option>
                  {envNames.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
          )}
          <Field label="Network permission (one host per line)" hint={networkHost(form.url) ? `Leave empty to allow ${networkHost(form.url)}.` : "The hosts the tool may reach."}>
            <Textarea rows={2} value={form.networkText} onChange={(e) => set({ networkText: e.target.value })} placeholder={networkHost(form.url) || "tools.example.com"} className="font-mono text-xs" />
          </Field>
        </div>
      )}

      {error && <ErrorBox>{error}</ErrorBox>}
      <div className="flex justify-end">
        <Button variant="primary" onClick={() => void build()} disabled={building || uploading !== null}>
          {building ? "Building…" : "Build package"}
        </Button>
      </div>

      {result && (
        <div className="grid gap-3 border-t border-[var(--color-border)] pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <p className="min-w-0 flex-1 text-sm font-medium text-[var(--color-fg)]">{result.file}</p>
            <Button variant="secondary" size="sm" onClick={download}>
              <DownloadSimpleIcon size={14} /> Download
            </Button>
            <Button variant="ghost" size="sm" onClick={copy}>
              <CopySimpleIcon size={14} /> Copy
            </Button>
          </div>
          <pre className="max-h-72 overflow-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 font-mono text-xs">{result.json}</pre>
          <div className="text-sm text-[var(--color-fg-muted)]">
            <p>The console does not sign packages. Sign and install it on the machine that holds your publisher key:</p>
            <pre className="mt-2 overflow-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 font-mono text-xs">{signSteps(result.file).join("\n")}</pre>
            <p className="mt-2">Or sign the file with your own tool and publish it with its detached signature in the publish flow above.</p>
          </div>
        </div>
      )}
    </div>
  );
}
