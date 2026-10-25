import { useCallback, useEffect, useState } from "react";
import { assistModelApi, type AssistModelConfig, type AssistModelTest } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { hostList } from "@/lib/hosts";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// PlatformAssistModel is the platform console's "AI assist" card
// (docs/prd/2026-10-13-ai-assist.md §2): the deployment's assist model, an
// OpenAI-compatible chat completions base URL (a LiteLLM proxy, say), the
// model and an API key. The key is write-only: the card shows whether one
// is set, and replaces or clears it. Test sends one short completion. An
// empty base URL turns assists off; without the secret store key the model
// cannot be configured (the key is never stored in clear).

export function PlatformAssistModel() {
  const toast = useToast();
  const [saved, setSaved] = useState<AssistModelConfig | null>(null);
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  // The hosts an organization's own model may use (one per line or comma).
  const [orgHosts, setOrgHosts] = useState("");
  // The key being typed: "" keeps the stored one.
  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<AssistModelTest | null>(null);

  const adopt = (c: AssistModelConfig) => {
    setSaved(c);
    setBaseUrl(c.baseUrl);
    setModel(c.model);
    setOrgHosts((c.orgHosts ?? []).join(", "));
    setApiKey("");
    setReplacing(false);
  };

  const load = useCallback(async () => {
    setError(null);
    try {
      adopt(await assistModelApi.get());
    } catch (e) {
      setError(errorText(e, "Could not load the assist model"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (clearKey = false) => {
    setSaving(true);
    setError(null);
    setTest(null);
    try {
      adopt(
        await assistModelApi.put({
          baseUrl: baseUrl.trim(),
          model: model.trim(),
          orgHosts: hostList(orgHosts),
          ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}),
          ...(clearKey ? { clearKey: true } : {}),
        }),
      );
      toast.success(clearKey ? "The API key is cleared" : baseUrl.trim() ? "The assist model is saved" : "AI assist is off");
    } catch (e) {
      setError(errorText(e, "Could not save the assist model"));
    } finally {
      setSaving(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await assistModelApi.test());
    } catch (e) {
      setTest({ ok: false, error: errorText(e, "The test failed"), usage: { model: "", promptTokens: 0, completionTokens: 0, latencyMs: 0 } });
    } finally {
      setTesting(false);
    }
  };

  if (!saved) {
    return error ? <ErrorBox>{error}</ErrorBox> : <p className="text-sm text-[var(--color-fg-muted)]">Loading the assist model…</p>;
  }
  const dirty =
    baseUrl.trim() !== saved.baseUrl || model.trim() !== saved.model || !!apiKey.trim() || hostList(orgHosts).join(",") !== (saved.orgHosts ?? []).join(",");
  const on = !!saved.baseUrl && !!saved.model;
  return (
    <section className="flex flex-col gap-4">
      {error && <ErrorBox>{error}</ErrorBox>}
      <Card className="p-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">Assist model</h2>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              The fast model behind every AI assist (the composer's Enhance prompt, Fill with AI). Any OpenAI-compatible chat completions endpoint. Organizations can turn assists off in their settings.
            </p>
          </div>
          <Badge tone={on ? "accent" : "neutral"}>{on ? "On" : "Off"}</Badge>
        </div>
        {!saved.configurable ? (
          <p className="mt-4 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-sm text-[var(--color-warning)]">
            The gateway has no secret store key (BNBLOC_SECRET_STORE_KEY), so the model's API key cannot be stored. Set it on the gateway to configure AI assist.
          </p>
        ) : (
          <div className="mt-4 grid gap-4 md:grid-cols-2">
            <Field label="Base URL" hint="Up to the version path, e.g. https://llm.example.com/v1. Empty turns AI assist off.">
              <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://llm.example.com/v1" />
            </Field>
            <Field label="Model" hint="A fast, inexpensive model: assists answer in seconds.">
              <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder="gpt-4o-mini" />
            </Field>
            <Field label="API key" hint={saved.keySet && !replacing ? "Stored sealed; never shown again." : "Sent once and stored sealed."}>
              {saved.keySet && !replacing ? (
                <div className="flex items-center gap-2">
                  <Badge tone="accent">Set</Badge>
                  <Button variant="ghost" size="sm" onClick={() => setReplacing(true)}>
                    Replace
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => void save(true)} disabled={saving}>
                    Clear
                  </Button>
                </div>
              ) : (
                <Input type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={saved.keySet ? "A new key" : "sk-…"} />
              )}
            </Field>
            <Field label="Organization hosts" hint="Hosts an organization may use for its own model (comma or one per line). None: organizations use this model.">
              <Textarea rows={2} value={orgHosts} onChange={(e) => setOrgHosts(e.target.value)} placeholder="llm.example.com" />
            </Field>
            {saved.updatedBy && (
              <p className="self-end text-xs text-[var(--color-fg-subtle)]">
                Last changed by {saved.updatedBy}
                {saved.updatedAtMs ? ` ${relativeTime(saved.updatedAtMs)}` : ""}
              </p>
            )}
          </div>
        )}
        {test && (
          <div className="mt-4">
            {test.ok ? (
              <p className="text-sm text-[var(--color-fg-muted)]">
                Answered in {test.usage.latencyMs} ms by {test.usage.model || saved.model}
                {test.reply ? `: “${test.reply}”` : ""}
              </p>
            ) : (
              <ErrorBox>{test.error || "The test failed"}</ErrorBox>
            )}
          </div>
        )}
        {saved.configurable && (
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="ghost" onClick={() => void runTest()} disabled={testing || dirty || !on}>
              {testing ? "Testing…" : "Test"}
            </Button>
            {dirty && (
              <Button variant="ghost" onClick={() => adopt(saved)} disabled={saving}>
                Discard
              </Button>
            )}
            <Button variant="primary" onClick={() => void save()} disabled={saving || !dirty}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </Card>
    </section>
  );
}
