import { useCallback, useEffect, useState } from "react";
import { errorText } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { hostList } from "@/lib/hosts";
import { serverEgressApi, type ServerEgress } from "@/lib/serverEgress";
import { ServerEgressAllowlist, ServerEgressPublic } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

// PlatformServerEgress is the platform console's Network card
// (docs/architecture/server-egress.md): where the platform's own
// server-side requests may go (today the documentation pages an AI assist
// reads). Public, the default, needs nothing; hosts are listed where an
// external name answers on a private address (an egress proxy behind a DNS
// allowlist), and Listed hosts only reaches nothing else.

export function PlatformServerEgress() {
  const toast = useToast();
  const [saved, setSaved] = useState<ServerEgress | null>(null);
  const [mode, setMode] = useState<string>(ServerEgressPublic);
  const [hosts, setHosts] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const adopt = (p: ServerEgress) => {
    setSaved(p);
    setMode(p.mode);
    setHosts(p.hosts.join("\n"));
  };

  const load = useCallback(async () => {
    setError(null);
    try {
      adopt(await serverEgressApi.get());
    } catch (e) {
      setError(errorText(e, "Could not load the server network"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      adopt(await serverEgressApi.put({ mode, hosts: hostList(hosts) }));
      toast.success("The server network is saved");
    } catch (e) {
      setError(errorText(e, "Could not save the server network"));
    } finally {
      setSaving(false);
    }
  };

  if (!saved) {
    return error ? <ErrorBox>{error}</ErrorBox> : <p className="text-sm text-[var(--color-fg-muted)]">Loading the server network…</p>;
  }
  const dirty = mode !== saved.mode || hostList(hosts).join(",") !== saved.hosts.join(",");
  const listedOnly = mode === ServerEgressAllowlist;
  return (
    <section className="flex flex-col gap-4">
      {error && <ErrorBox>{error}</ErrorBox>}
      <Card className="p-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">Server network</h2>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              Where the platform's own servers may send requests: today the documentation pages an AI assist reads from a link. An address inside your network is never reached unless its host is listed here.
            </p>
          </div>
          <Badge tone={listedOnly ? "accent" : "neutral"}>{listedOnly ? "Listed hosts only" : "Public internet"}</Badge>
        </div>
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <Field label="Reach" hint={listedOnly ? "Only the hosts below; no other name is even looked up." : "Any host on a public address, and the hosts below."}>
            <Select value={mode} onChange={(e) => setMode(e.target.value)} aria-label="Reach">
              <option value={ServerEgressPublic}>The public internet</option>
              <option value={ServerEgressAllowlist}>Listed hosts only</option>
            </Select>
          </Field>
          <Field label="Hosts" hint="Exact names, one per line. List an external site that your network answers on a private address (an egress proxy).">
            <Textarea rows={4} value={hosts} onChange={(e) => setHosts(e.target.value)} placeholder="docs.example.com" />
          </Field>
          {saved.updatedBy && (
            <p className="self-end text-xs text-[var(--color-fg-subtle)]">
              Last changed by {saved.updatedBy}
              {saved.updatedAtMs ? ` ${relativeTime(saved.updatedAtMs)}` : ""}
            </p>
          )}
        </div>
        <div className="mt-4 flex justify-end gap-2">
          {dirty && (
            <Button variant="ghost" onClick={() => adopt(saved)} disabled={saving}>
              Discard
            </Button>
          )}
          <Button variant="primary" onClick={() => void save()} disabled={saving || !dirty}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
      </Card>
    </section>
  );
}
