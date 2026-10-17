import { useCallback, useEffect, useState } from "react";
import { MEDIA_ADAPTER_KINDS, mediaProviderApi } from "@/lib/media";
import { errorText } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

// PlatformMediaProviders is the platform console's "Media providers" card
// (docs/prd/2026-10-16-voice-and-image-generation.md §2): one provider per
// capability — transcribe (speech to text in the composer, app.ai.voice)
// and image (agents' generate_image, app.ai.image). A provider is a wire
// protocol family (kind), the endpoint, the model and a sealed key, with
// the adapter's own knobs as params. Test makes one real short call.

function MediaProviderFields({ cap, label, hint }: { readonly cap: "transcribe" | "image"; readonly label: string; readonly hint: string }) {
  const toast = useToast();
  const api = mediaProviderApi(cap);
  const [saved, setSaved] = useState<Awaited<ReturnType<typeof api.get>> | null>(null);
  const [kind, setKind] = useState<string>(MEDIA_ADAPTER_KINDS[0]);
  const [baseUrl, setBaseUrl] = useState("");
  const [models, setModels] = useState("");
  const [orgHosts, setOrgHosts] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<Awaited<ReturnType<typeof api.test>> | null>(null);

  const adopt = (c: Awaited<ReturnType<typeof api.get>>) => {
    setSaved(c);
    setKind(c.kind || MEDIA_ADAPTER_KINDS[0]);
    setBaseUrl(c.baseUrl);
    setModels((c.models ?? []).join(", "));
    setOrgHosts((c.orgHosts ?? []).join(", "));
    setApiKey("");
    setReplacing(false);
  };

  const load = useCallback(async () => {
    setError(null);
    try {
      adopt(await api.get());
    } catch (e) {
      setError(errorText(e, "Could not load the provider"));
    }
  }, [cap]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!saved) {
    return error ? <ErrorBox>{error}</ErrorBox> : <p className="text-sm text-[var(--color-fg-muted)]">Loading…</p>;
  }
  const dirty =
    kind !== saved.kind || baseUrl.trim() !== saved.baseUrl || !!apiKey.trim() ||
    orgHostList(orgHosts).join(",") !== (saved.orgHosts ?? []).join(",") ||
    orgHostList(models).join(",") !== (saved.models ?? []).join(",");
  const on = !!saved.baseUrl && !!saved.model;

  const save = async (clearKey = false) => {
    setSaving(true);
    setError(null);
    setTest(null);
    try {
      const params: Record<string, string> = {};
      for (const [k, v] of Object.entries(saved.params ?? {})) {
        if (k !== "image.size.square" && k !== "image.size.landscape" && k !== "image.size.portrait" && !k.startsWith("transcribe.")) continue;
        if (v) params[k] = v;
      }
      const modelList = orgHostList(models);
      adopt(
        await api.put({
          kind,
          baseUrl: baseUrl.trim(),
          model: modelList[0] ?? "",
          models: modelList,
          orgHosts: orgHostList(orgHosts),
          params,
          ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}),
          ...(clearKey ? { clearKey: true } : {}),
        }),
      );
      toast.success(clearKey ? "The API key is cleared" : baseUrl.trim() ? `${label} is saved` : `${label} is off`);
    } catch (e) {
      setError(errorText(e, "Could not save the provider"));
    } finally {
      setSaving(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.test());
    } catch (e) {
      setTest({ ok: false, error: errorText(e, "The test failed"), latencyMs: 0 });
    } finally {
      setTesting(false);
    }
  };

  return (
    <Card className="p-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-[var(--color-fg)]">{label}</h3>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{hint}</p>
        </div>
        <Badge tone={on ? "accent" : "neutral"}>{on ? "On" : "Off"}</Badge>
      </div>
      {!saved.configurable ? (
        <p className="mt-4 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-sm text-[var(--color-warning)]">
          The gateway has no secret store key (BNBLOC_SECRET_STORE_KEY), so the provider's API key cannot be stored.
        </p>
      ) : (
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <Field label="Wire protocol" hint="The provider's API family.">
            <Select value={kind} onChange={(e) => setKind(e.target.value)}>
              {MEDIA_ADAPTER_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Base URL" hint="Up to the version path, e.g. https://llm.example.com/v1. Empty turns this capability off.">
            <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://llm.example.com/v1" />
          </Field>
          <Field label="Models" hint="The models the provider serves (comma-separated; the first is the default callers get).">
            <Input value={models} onChange={(e) => setModels(e.target.value)} placeholder="gemini-nano-banana-2.1, gpt-image-2" />
          </Field>
          <Field label="Organization hosts" hint="Hosts organizations may use for their own provider (comma or one per line). None: organizations use this provider.">
            <Input value={orgHosts} onChange={(e) => setOrgHosts(e.target.value)} placeholder="llm.example.com" />
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
          {saved.updatedBy && (
            <p className="self-end text-xs text-[var(--color-fg-subtle)]">
              Last changed by {saved.updatedBy}
              {saved.updatedAtMs ? ` ${relativeTime(saved.updatedAtMs)}` : ""}
            </p>
          )}
        </div>
      )}
      {test && (
        <div className="mt-3">
          {test.ok ? (
            <p className="text-sm text-[var(--color-fg-muted)]">
              Answered in {test.latencyMs} ms{test.reply ? `: “${test.reply}”` : ""}
            </p>
          ) : (
            <ErrorBox>{test.error || "The test failed"}</ErrorBox>
          )}
        </div>
      )}
      {saved.configurable && (
        <div className="mt-3 flex flex-wrap justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => void runTest()} disabled={testing || dirty || !on}>
            {testing ? "Testing…" : "Test"}
          </Button>
          <Button variant="primary" size="sm" onClick={() => void save()} disabled={saving || !dirty}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
      )}
    </Card>
  );
}

// orgHostList splits the org-hosts input (comma or space separated).
function orgHostList(text: string): string[] {
  return text
    .split(/[,\s]+/)
    .map((h) => h.trim())
    .filter(Boolean);
}

// PlatformMediaProviders is the two capability cards.
export function PlatformMediaProviders() {
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">Media providers</h2>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
          The models behind voice input in the composer (speech to text) and image generation in rooms and apps. Any provider whose API follows one of the wire protocol families below.
        </p>
      </div>
      <MediaProviderFields
        cap="transcribe"
        label="Speech to text"
        hint="Voice input in the composer and app.ai.voice send audio here; agents read recordings through the transcribe_audio tool."
      />
      <MediaProviderFields
        cap="image"
        label="Image generation"
        hint="Agents' generate_image and app.ai.image draw here; the image lands in the room's Docs."
      />
    </section>
  );
}
