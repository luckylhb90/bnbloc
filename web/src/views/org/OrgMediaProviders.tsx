import { useCallback, useEffect, useState } from "react";
import { mediaProviderApi, type MediaProviderApi, type MediaProviderUpdateBody, type OrgMediaProviderView } from "@/lib/media";
import { decisionProviderApi } from "@/lib/decisions";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// OrgMediaProviders is the organization page's own media providers (the
// assist org-model pattern): an org admin may replace the platform's
// providers for the organization's members with their own, on a host the
// platform allows. Hidden when the platform allows none. An empty base URL
// goes back to the platform's provider.

// OrgProviderFields is one provider (a media capability or the decision
// model: the same settings shape). id keys the reload.
function OrgProviderFields({ id, api, label }: { readonly id: string; readonly api: MediaProviderApi; readonly label: string }) {
  const toast = useToast();
  const [saved, setSaved] = useState<OrgMediaProviderView | null>(null);
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  const [kind, setKind] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      // The org GET answers the MediaOrgProvider shape: provider,
      // allowedHosts, platformModel.
      const org = (await api.get()) as unknown as OrgMediaProviderView;
      setSaved(org);
      setBaseUrl(org.provider.baseUrl);
      setModel(org.provider.model);
      setKind(org.provider.kind);
      setApiKey("");
      setReplacing(false);
    } catch {
      setSaved(null); // no org provider surface (an older gateway, or none allowed)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!saved || saved.allowedHosts.length === 0) return null;
  const cfg = saved.provider;
  const own = !!cfg.baseUrl;

  const save = async (opts: { clearKey?: boolean; off?: boolean } = {}) => {
    setSaving(true);
    setError(null);
    try {
      const body: MediaProviderUpdateBody = opts.off
        ? { kind, baseUrl: "", model: "", clearKey: true }
        : { kind, baseUrl: baseUrl.trim(), model: model.trim(), ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}), ...(opts.clearKey ? { clearKey: true } : {}) };
      await api.put(body);
      await load();
      toast.success(opts.off ? "Members use the platform's provider again" : `${label} is saved`);
    } catch (e) {
      setError(errorText(e, "Could not save the provider"));
    } finally {
      setSaving(false);
    }
  };

  const dirty = baseUrl.trim() !== cfg.baseUrl || model.trim() !== cfg.model || !!apiKey.trim() || kind !== cfg.kind;
  return (
    <div className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{label}</span>
        <Badge tone={own ? "accent" : "neutral"}>{own ? "Own provider" : saved.platformModel ? `Platform: ${saved.platformModel}` : "Platform's"}</Badge>
      </div>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Optional: your own provider replaces the platform's for this organization's members. Allowed hosts: {saved.allowedHosts.join(", ")}.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}
      {!cfg.configurable ? (
        <p className="text-xs text-[var(--color-warning)]">The gateway has no secret store key, so a provider's API key cannot be stored.</p>
      ) : (
        <>
          <Field label="Wire protocol" hint="The provider's API family.">
            <Input value={kind} onChange={(e) => setKind(e.target.value)} placeholder="openai-compatible" />
          </Field>
          <Field label="Base URL" hint="Empty: use the platform's provider">
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
          <div className="flex flex-wrap justify-end gap-2">
            {own && (
              <Button variant="ghost" size="sm" onClick={() => void save({ off: true })} disabled={saving}>
                Use the platform's
              </Button>
            )}
            <Button variant="primary" size="sm" onClick={() => void save()} disabled={saving || !dirty}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

// OrgMediaProviders is the media capabilities' cards and the decision
// model's.
export function OrgMediaProviders() {
  return (
    <div className="mt-2 flex flex-col gap-3">
      <OrgProviderFields id="transcribe" api={mediaProviderApi("transcribe", "org")} label="Speech to text" />
      <OrgProviderFields id="image" api={mediaProviderApi("image", "org")} label="Image generation" />
      <OrgProviderFields id="video" api={mediaProviderApi("video", "org")} label="Video generation" />
      <OrgProviderFields id="decide" api={decisionProviderApi("org")} label="Decision model" />
    </div>
  );
}
