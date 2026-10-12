import { useCallback, useEffect, useState } from "react";
import { CopyIcon, DownloadSimpleIcon, GlobeIcon } from "@phosphor-icons/react";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { promoteCommands, tenantScopeBody, tenantScopeLabel, type ContentAdminComponent } from "@/lib/contentSharing";
import { contentSharingApi } from "@/lib/contentSharingApi";
import { relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";
import { PermissionBadges } from "@/views/library/ContentViewDialog";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

// Super-admin: content at platform scope (docs/prd/2026-10-12-experts-and-
// teams.md, step 4). Platform content reaches every organization; it gets
// there the way executable components do, as a package signed offline and
// installed (the console never signs). Organization content of every
// organization is listed to promote: "Promote" explains the commands and
// downloads the exact package to sign.
export function PlatformContentTab() {
  const toast = useToast();
  const [items, setItems] = useState<readonly ContentAdminComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [promoting, setPromoting] = useState<ContentComponent | null>(null);
  const [removing, setRemoving] = useState<ContentComponent | null>(null);
  const [scoping, setScoping] = useState<ContentAdminComponent | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await contentSharingApi.platformContent();
      setItems(r.components ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load content"));
      setItems([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const setStatus = async (c: ContentComponent, action: "unpublish" | "restore") => {
    try {
      await contentSharingApi.setPlatformStatus(c.id, action);
      toast.success(action === "unpublish" ? `${contentLabel(c)} is unpublished everywhere` : `${contentLabel(c)} is published again`);
      await load();
    } catch (e) {
      toast.error(errorText(e, "Could not change it"));
    }
  };
  const remove = async (c: ContentComponent) => {
    try {
      await contentSharingApi.removePlatform(c.id);
      toast.success(`${contentLabel(c)} is removed from the platform`);
      await load();
    } catch (e) {
      toast.error(errorText(e, "Could not remove it"));
    }
  };

  const platform = (items ?? []).filter((i) => i.component.scope === "platform");
  const org = (items ?? []).filter((i) => i.component.scope === "org");
  return (
    <section className="flex flex-col gap-6 p-6">
      {error && <ErrorBox>{error}</ErrorBox>}
      <div className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-semibold">Platform content</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
            Experts, skill packs and prompt packs every organization's members find in their Library. Each version was signed by a trusted publisher key.
          </p>
        </div>
        {items && platform.length === 0 && <p className="text-sm text-[var(--color-fg-subtle)]">None yet. Promote one of the organizations' below.</p>}
        {platform.map((item) => {
          const c = item.component;
          return (
          <Card key={c.id} className="flex flex-wrap items-center gap-3 p-3">
            <ContentTitle component={c} />
            <PermissionBadges component={c} />
            {c.status === "retired" ? <Badge>Unpublished</Badge> : <Badge tone="accent">Published</Badge>}
            <Badge title="The organizations whose members get it">{tenantScopeLabel(item.enabledTenants)}</Badge>
            <Button size="sm" variant="ghost" onClick={() => setScoping(item)}>
              Organizations
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setPromoting(c)}>
              New version
            </Button>
            {c.status === "retired" ? (
              <Button size="sm" variant="outline" onClick={() => void setStatus(c, "restore")}>
                Restore
              </Button>
            ) : (
              <Button size="sm" variant="outline" onClick={() => void setStatus(c, "unpublish")}>
                Unpublish
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => setRemoving(c)}>
              Remove
            </Button>
          </Card>
          );
        })}
      </div>
      <div className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-semibold">Organization content</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">Approved by an organization's admins. Promote one to reach every organization.</p>
        </div>
        {items && org.length === 0 && <p className="text-sm text-[var(--color-fg-subtle)]">No organization has published content yet.</p>}
        {org.map(({ component: c, tenantId }) => (
          <Card key={c.id} className="flex flex-wrap items-center gap-3 p-3">
            <ContentTitle component={c} tenantId={tenantId} />
            <PermissionBadges component={c} />
            <Button size="sm" variant="outline" onClick={() => setPromoting(c)}>
              <GlobeIcon size={14} />
              Promote
            </Button>
          </Card>
        ))}
      </div>
      <PromoteDialog component={promoting} onClose={() => setPromoting(null)} />
      <TenantScopeDialog item={scoping} onClose={() => setScoping(null)} onSaved={() => void load()} />
      <ConfirmDialog
        open={!!removing}
        onClose={() => setRemoving(null)}
        onConfirm={() => removing && void remove(removing)}
        title="Remove from the platform?"
        description={removing ? `${contentLabel(removing)} is deleted for every organization, and rooms using it lose it.` : undefined}
        confirmLabel="Remove"
        danger
      />
    </section>
  );
}

function ContentTitle({ component: c, tenantId }: { readonly component: ContentComponent; readonly tenantId?: string }) {
  return (
    <div className="min-w-0 flex-1">
      <p className="truncate text-sm font-medium">
        {contentLabel(c)} <span className="font-mono text-xs text-[var(--color-fg-subtle)]">v{c.latestVersion}</span>
      </p>
      <p className="truncate text-xs text-[var(--color-fg-subtle)]">
        {c.kind}
        {c.owner && (
          <>
            {" · by "}
            <UserName id={c.owner} />
          </>
        )}
        {tenantId ? ` · ${tenantId}` : ""} · {relativeTime(c.updatedAtMs)}
      </p>
    </div>
  );
}

// PromoteDialog explains the offline signing steps for one version and
// downloads the exact package to sign.
function PromoteDialog({ component: c, onClose }: { readonly component: ContentComponent | null; readonly onClose: () => void }) {
  const toast = useToast();
  const copy = useCopy();
  const [publisher, setPublisher] = useState("");
  const [busy, setBusy] = useState(false);
  if (!c) return null;
  const commands = promoteCommands(c).join("\n");
  const download = async () => {
    setBusy(true);
    try {
      const blob = await contentSharingApi.exportPackage(c.id, c.latestVersion, publisher);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${c.name}-${c.latestVersion}.package.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      toast.error(errorText(e, "Could not export the package"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={`Promote ${contentLabel(c)} v${c.latestVersion} to every organization`}
      description="Platform content is signed offline with a trusted publisher key, like any component. Run the command below where your publisher key is (it exports, signs and installs), or download the package and sign it with your own tool."
      footer={
        <>
          <Button
            variant="ghost"
            onClick={() => void copy(commands, "commands")}
          >
            <CopyIcon size={14} />
            Copy commands
          </Button>
          <div className="flex-1" />
          <Button variant="primary" onClick={() => void download()} disabled={busy}>
            <DownloadSimpleIcon size={14} />
            Download package
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <pre className="overflow-x-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 font-mono text-xs leading-5">{commands}</pre>
        <Field label="Publisher (for the downloaded package)" hint="Your publisher id, written into the package. Default: self.">
          <Input value={publisher} onChange={(e) => setPublisher(e.target.value)} placeholder="self" />
        </Field>
        <p className="text-xs text-[var(--color-fg-subtle)]">
          Members of every organization find it in their Library once installed. Someone else's scripts or local tools still run sandboxed.
        </p>
      </div>
    </Dialog>
  );
}

// TenantScopeDialog limits platform content to some organizations, or
// gives it to every organization again (the component tenant scope).
function TenantScopeDialog({ item, onClose, onSaved }: { readonly item: ContentAdminComponent | null; readonly onClose: () => void; readonly onSaved: () => void }) {
  const toast = useToast();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setText((item?.enabledTenants ?? []).join(", ")), [item]);
  if (!item) return null;
  const c = item.component;
  const body = tenantScopeBody(text);
  const save = async () => {
    setBusy(true);
    try {
      await contentSharingApi.setPlatformTenants(c.name, c.kind, body);
      toast.success("allTenants" in body ? `${contentLabel(c)} reaches every organization` : `${contentLabel(c)} reaches ${tenantScopeLabel(body.enabledTenants).toLowerCase()}`);
      onSaved();
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not change its organizations"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Organizations for ${contentLabel(c)}`}
      description="Members of these organizations find it in their Library and can use it. Leave the list empty to give it to every organization."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy}>
            {busy ? "Saving…" : "allTenants" in body ? "Give to every organization" : "Limit to these"}
          </Button>
        </>
      }
    >
      <Field label="Organization ids" hint="Separated by commas. Rooms that already use it keep their pinned version.">
        <Input value={text} onChange={(e) => setText(e.target.value)} placeholder="Every organization" autoFocus />
      </Field>
    </Dialog>
  );
}
