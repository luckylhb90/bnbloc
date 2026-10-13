import { useCallback, useEffect, useState } from "react";
import { orgAssistModelApi, type AssistModelTest, type AssistOrgModel } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// OrgAssistModel is the organization page's own assist model (docs/prd/
// 2026-10-13-ai-assist.md §6): an org admin may replace the platform's
// model for the organization's members with their own (base URL, model,
// key sealed per organization) on a host the platform allows. Hidden when
// the platform allows none. An empty base URL goes back to the platform's
// model; the organization's AI assist switch still turns assists off.

export function OrgAssistModel() {
  const toast = useToast();
  const [saved, setSaved] = useState<AssistOrgModel | null>(null);
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<AssistModelTest | null>(null);

  const adopt = (m: AssistOrgModel) => {
    setSaved(m);
    setBaseUrl(m.model.baseUrl);
    setModel(m.model.model);
    setApiKey("");
    setReplacing(false);
  };

  const load = useCallback(async () => {
    try {
      adopt(await orgAssistModelApi.get());
    } catch {
      // No organization model to offer (an older gateway, or none allowed).
      setSaved(null);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (!saved || saved.allowedHosts.length === 0) return null;
  const cfg = saved.model;
  const own = !!cfg.baseUrl;

  const save = async (opts: { clearKey?: boolean; off?: boolean } = {}) => {
    setSaving(true);
    setError(null);
    setTest(null);
    try {
      const body = opts.off
        ? { baseUrl: "", model: "", clearKey: true }
        : { baseUrl: baseUrl.trim(), model: model.trim(), ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}), ...(opts.clearKey ? { clearKey: true } : {}) };
      adopt(await orgAssistModelApi.put(body));
      toast.success(opts.off ? "Members use the platform's model again" : opts.clearKey ? "The API key is cleared" : "The organization's model is saved");
    } catch (e) {
      setError(errorText(e, "Could not save the organization's model"));
    } finally {
      setSaving(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await orgAssistModelApi.test());
    } catch (e) {
      setTest({ ok: false, error: errorText(e, "The test failed"), usage: { model: "", promptTokens: 0, completionTokens: 0, latencyMs: 0 } });
    } finally {
      setTesting(false);
    }
  };

  const dirty = baseUrl.trim() !== cfg.baseUrl || model.trim() !== cfg.model || !!apiKey.trim();
  return (
    <div className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">Organization model</span>
        <Badge tone={own ? "accent" : "neutral"}>{own ? "Own model" : saved.platformModel ? `Platform: ${saved.platformModel}` : "Platform's"}</Badge>
      </div>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Optional: your own OpenAI-compatible model replaces the platform's{saved.platformModel ? ` (${saved.platformModel})` : ""} for this organization's members.
        Allowed hosts: {saved.allowedHosts.join(", ")}.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}
      {!cfg.configurable ? (
        <p className="text-xs text-[var(--color-warning)]">The gateway has no secret store key, so a model's API key cannot be stored.</p>
      ) : (
        <>
          <Field label="Base URL" hint="Empty: use the platform's model">
            <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder={`https://${saved.allowedHosts[0]}/v1`} />
          </Field>
          <Field label="Model">
            <Input value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
          <Field label="API key">
            {cfg.keySet && !replacing ? (
              <div className="flex items-center gap-2">
                <Badge tone="accent">Set</Badge>
                <Button variant="ghost" size="sm" onClick={() => setReplacing(true)}>
                  Replace
                </Button>
                <Button variant="ghost" size="sm" onClick={() => void save({ clearKey: true })} disabled={saving}>
                  Clear
                </Button>
              </div>
            ) : (
              <Input type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={cfg.keySet ? "A new key" : "sk-…"} />
            )}
          </Field>
          {test &&
            (test.ok ? (
              <p className="text-xs text-[var(--color-fg-muted)]">
                Answered in {test.usage.latencyMs} ms by {test.usage.model || cfg.model}
                {test.reply ? `: “${test.reply}”` : ""}
              </p>
            ) : (
              <ErrorBox>{test.error || "The test failed"}</ErrorBox>
            ))}
          <div className="flex flex-wrap justify-end gap-2">
            {own && (
              <Button variant="ghost" size="sm" onClick={() => void save({ off: true })} disabled={saving}>
                Use the platform's
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={() => void runTest()} disabled={testing || dirty || !own}>
              {testing ? "Testing…" : "Test"}
            </Button>
            <Button variant="primary" size="sm" onClick={() => void save()} disabled={saving || !dirty}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
