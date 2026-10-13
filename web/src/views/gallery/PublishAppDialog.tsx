import { useEffect, useRef, useState } from "react";
import { AppWindowIcon, BuildingsIcon } from "@phosphor-icons/react";
import { APP_CATEGORY_LABELS, publishAppForm, publishAppProblem, publishAppRequest, type AppCategory, type PublishAppForm } from "@/lib/appGallery";
import { appGalleryApi } from "@/lib/appGalleryApi";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { invalidateQueries } from "@/lib/useQuery";
import { AppCategoryValues } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { PublishDialog } from "@/views/library/ContentSharing";
import { GALLERY_QUERY } from "./AppParts";

// PublishAppDialog is "Publish to gallery" (docs/prd/2026-10-13-app-gallery.md
// §6), from Library → Apps and from an app deliverable's card in its room:
// the site's current version (its files, cover and README, never its data)
// becomes an app of the member's — a new one, or the next version of one
// of theirs — with a name, display name, description, category and tags
// (POST …/publish-app; the form and its request are lib/appGallery.ts).
// Then it offers "Publish to organization" through the Library's publish
// flow (PublishDialog: sent for approval, or published at once, by the
// organization's setting).

/** The site a publish snapshots: a room's app deliverable. */
export interface PublishSite {
  readonly roomId: string;
  readonly artifactId: string;
  readonly title: string;
}

export function PublishAppDialog({ site, onClose }: { readonly site: PublishSite | null; readonly onClose: () => void }) {
  const toast = useToast();
  const [mine, setMine] = useState<ContentComponent[] | null>(null);
  const [form, setForm] = useState<PublishAppForm>(publishAppForm("", []));
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<ContentComponent | null>(null);
  const [publishing, setPublishing] = useState(false);
  // The dialog restarts when it opens for another site, not on a new
  // object for the same one.
  const key = site ? `${site.roomId}\u0000${site.artifactId}` : "";
  const latest = useRef(site);
  latest.current = site;

  useEffect(() => {
    const site = latest.current;
    if (!key || !site) return;
    let live = true;
    setSaved(null);
    setFailure("");
    setMine(null);
    setForm(publishAppForm(site.title, []));
    contentApi
      .list("app")
      .then((r) => {
        if (!live) return;
        const own = (r.components ?? []).filter((c) => c.mine);
        setMine(own);
        setForm(publishAppForm(site.title, own));
      })
      .catch(() => live && setMine([]));
    return () => {
      live = false;
    };
  }, [key]);

  if (!site) return null;
  const set = (patch: Partial<PublishAppForm>) => setForm((f) => ({ ...f, ...patch }));
  const problem = publishAppProblem(form);
  const target = mine?.find((c) => c.id === form.componentId);
  const submit = async () => {
    if (problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const d = await appGalleryApi.publish(site.roomId, site.artifactId, publishAppRequest(form));
      invalidateQueries(GALLERY_QUERY);
      toast.success(`${contentLabel(d.component)} v${d.component.latestVersion} is in the gallery`);
      setSaved(d.component);
    } catch (e) {
      setFailure(errorText(e, "Could not publish the app"));
    } finally {
      setBusy(false);
    }
  };

  if (saved) {
    return (
      <>
        <Dialog
          open={!publishing}
          onClose={onClose}
          title={`${contentLabel(saved)} is in the gallery`}
          description={`Version ${saved.latestVersion} is yours in the app gallery (Plaza → Apps): its files, cover and README, never its data. Publish it to your organization so everyone there can find it, try it and use a copy.`}
          footer={
            <>
              <Button variant="ghost" onClick={onClose}>
                Done
              </Button>
              <Button variant="primary" onClick={() => setPublishing(true)}>
                <BuildingsIcon size={15} />
                Publish to organization…
              </Button>
            </>
          }
        />
        <PublishDialog component={publishing ? saved : null} onClose={onClose} onChanged={() => invalidateQueries(GALLERY_QUERY)} />
      </>
    );
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="Publish to gallery"
      description="The app's current version (its files, cover and README, never its data) becomes an app in the gallery, yours first. Others try a demo of it or use their own copy."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!!problem || busy || mine === null}>
            <AppWindowIcon size={15} />
            {busy ? "Publishing…" : target ? "Publish new version" : "Publish"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {mine && mine.length > 0 && (
          <Field label="Publish as" hint="A new app, or the next version of one of yours (rooms that use a copy keep theirs).">
            <Select value={form.componentId} onChange={(e) => set({ componentId: e.target.value })}>
              <option value="">A new app</option>
              {mine.map((c) => (
                <option key={c.id} value={c.id}>
                  New version of {contentLabel(c)} (now v{c.latestVersion})
                </option>
              ))}
            </Select>
          </Field>
        )}
        {!target && (
          <Field label="Name" hint="Lower-case letters, digits and hyphens: how the app is referred to.">
            <Input value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="lunch-poll" autoFocus />
          </Field>
        )}
        <Field label="Display name" hint={target ? "Left empty, the app keeps its own." : undefined}>
          <Input value={form.displayName} onChange={(e) => set({ displayName: e.target.value })} placeholder={target ? contentLabel(target) : "Lunch poll"} />
        </Field>
        <Field label="Description">
          <Textarea value={form.description} onChange={(e) => set({ description: e.target.value })} rows={3} maxLength={500} placeholder={target?.description || "What the app is for, in a sentence or two"} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Category">
            <Select value={form.category} onChange={(e) => set({ category: e.target.value as AppCategory | "" })}>
              <option value="">{target ? "Keep its category" : "No category"}</option>
              {AppCategoryValues.map((c) => (
                <option key={c} value={c}>
                  {APP_CATEGORY_LABELS[c]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Tags" hint="Separated by commas.">
            <Input value={form.tags} onChange={(e) => set({ tags: e.target.value })} placeholder="poll, team" />
          </Field>
        </div>
        {(failure || (problem && form.name.trim())) && <ErrorBox>{failure || problem}</ErrorBox>}
      </div>
    </Dialog>
  );
}
