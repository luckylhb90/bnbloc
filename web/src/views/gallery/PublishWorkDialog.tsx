import { useEffect, useRef, useState } from "react";
import { DESCRIBE_WORK, runJSONAssist, useAssistTask, workDraftPatch } from "@/lib/assist";
import { BuildingsIcon, StorefrontIcon } from "@phosphor-icons/react";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";
import { useMediaCapability } from "@/lib/media";
import { invalidateQueries } from "@/lib/useQuery";
import { WorkCategoryValues } from "@/lib/wire.gen";
import {
  publishWorkForm,
  publishWorkProblem,
  publishWorkRequest,
  recipeChips,
  WORK_CATEGORY_LABELS,
  type PublishWorkForm,
  type WorkCategory,
  type WorkRecipeCapture,
} from "@/lib/workGallery";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { AssistButton } from "@/views/AssistButton";
import { PublishDialog } from "@/views/library/ContentSharing";
import { GALLERY_QUERY } from "./WorkParts";

// PublishWorkDialog is "Publish to gallery" (app gallery PRD §6, works
// PRD §2–§3), from any deliverable's card in its room for the members who
// may post there and from Library → Works: the deliverable's current
// version (its files, or its one file, with a cover and README, never its
// data) becomes a work of the member's — a new one, or the next version of
// one of theirs — with a name, display name, description, category and
// tags (POST …/publish-work; the form and its request are
// lib/workGallery.ts). The recipe captured from the job that made it
// (GET …/recipe) is shown first, so nothing is published without the
// author seeing it: they edit the prompt, untick reference files or drop
// the recipe. Then it offers "Publish to organization" through the
// Library's publish flow (PublishDialog: sent for approval, or published at
// once, by the organization's setting). "Fill with AI" (the describe-work
// assist, when the deployment has one) drafts the name, description,
// category and tags from the deliverable; the member edits them, and
// nothing is published until they press Publish.

/** The deliverable a publish snapshots. */
export interface PublishDeliverable {
  readonly roomId: string;
  readonly artifactId: string;
  readonly title: string;
}

export function PublishWorkDialog({ deliverable, onClose }: { readonly deliverable: PublishDeliverable | null; readonly onClose: () => void }) {
  const toast = useToast();
  const [mine, setMine] = useState<ContentComponent[] | null>(null);
  const [capture, setCapture] = useState<WorkRecipeCapture | null>(null);
  const [form, setForm] = useState<PublishWorkForm>(publishWorkForm("", []));
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<ContentComponent | null>(null);
  const [publishing, setPublishing] = useState(false);
  const describe = useAssistTask(DESCRIBE_WORK);
  const [filling, setFilling] = useState(false);
  // An AI cover the person asked for (a data URL), sent with the publish.
  const [cover, setCover] = useState("");
  const [drawing, setDrawing] = useState(false);
  const canDraw = useMediaCapability("image") === true;
  // The dialog restarts when it opens for another deliverable, not on a new
  // object for the same one.
  const key = deliverable ? `${deliverable.roomId}\u0000${deliverable.artifactId}` : "";
  const latest = useRef(deliverable);
  latest.current = deliverable;

  useEffect(() => {
    const d = latest.current;
    if (!key || !d) return;
    let live = true;
    setSaved(null);
    setFailure("");
    setMine(null);
    setCapture(null);
    setCover("");
    setForm(publishWorkForm(d.title, []));
    void Promise.all([
      contentApi
        .list("work")
        .then((r) => (r.components ?? []).filter((c) => c.mine))
        .catch(() => [] as ContentComponent[]),
      workGalleryApi.recipe(d.roomId, d.artifactId).catch((e: unknown) => {
        if (live) setFailure(errorText(e, "Could not read how it was made"));
        return { version: 0 } as WorkRecipeCapture;
      }),
    ]).then(([own, captured]) => {
      if (!live) return;
      setMine(own);
      setCapture(captured);
      setForm(publishWorkForm(d.title, own, captured.recipe));
    });
    return () => {
      live = false;
    };
  }, [key]);

  if (!deliverable) return null;
  const set = (patch: Partial<PublishWorkForm>) => setForm((f) => ({ ...f, ...patch }));
  const problem = publishWorkProblem(form);
  const target = mine?.find((c) => c.id === form.componentId);
  const recipe = capture?.recipe;
  const submit = async () => {
    if (problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const d = await workGalleryApi.publish(deliverable.roomId, deliverable.artifactId, { ...publishWorkRequest(form, recipe), ...(cover ? { cover } : {}) });
      invalidateQueries(GALLERY_QUERY);
      toast.success(`${contentLabel(d.component)} v${d.component.latestVersion} is in the gallery`);
      setSaved(d.component);
    } catch (e) {
      setFailure(errorText(e, "Could not publish it"));
    } finally {
      setBusy(false);
    }
  };

  const fill = async () => {
    if (!describe || filling) return;
    setFilling(true);
    setFailure("");
    try {
      const r = await runJSONAssist(describe.name, { text: deliverable.title, roomId: deliverable.roomId, artifactId: deliverable.artifactId });
      setForm((f) => ({ ...f, ...workDraftPatch(r.fields ?? {}, f) }));
    } catch (e) {
      setFailure(errorText(e, "Could not fill it in with AI"));
    } finally {
      setFilling(false);
    }
  };

  const drawCover = async () => {
    if (drawing) return;
    setDrawing(true);
    setFailure("");
    try {
      const r = await workGalleryApi.makeCover(deliverable.roomId, deliverable.artifactId, {
        displayName: form.displayName.trim() || undefined,
        description: form.description.trim() || undefined,
      });
      setCover(r.cover);
    } catch (e) {
      setFailure(errorText(e, "Could not make a cover"));
    } finally {
      setDrawing(false);
    }
  };

  if (saved) {
    return (
      <>
        <Dialog
          open={!publishing}
          onClose={onClose}
          title={`${contentLabel(saved)} is in the gallery`}
          description={`Version ${saved.latestVersion} is yours in the works gallery (Plaza → Works): the deliverable as it is now, never its data${saved.recipe ? ", with its recipe" : ""}. Publish it to your organization so everyone there can find it and make something like it.`}
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

  const loading = mine === null || capture === null;
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title="Publish to gallery"
      description="The deliverable's current version (its files, never its data) becomes a work in the gallery, yours first. Others see it, and start from its recipe with Make similar."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!!problem || busy || loading}>
            <StorefrontIcon size={15} />
            {busy ? "Publishing…" : target ? "Publish new version" : "Publish"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {describe && (
          <div className="flex items-center justify-between gap-3 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] px-3 py-2">
            <p className="text-xs text-[var(--color-fg-muted)]">Draft the name, description, category and tags from the deliverable, then edit them.</p>
            <AssistButton variant="text" label="Fill with AI" state={filling ? "waiting" : "idle"} onRun={() => void fill()} disabled={loading || busy} />
          </div>
        )}
        {mine && mine.length > 0 && (
          <Field label="Publish as" hint="A new work, or the next version of one of yours (rooms that use a copy keep theirs).">
            <Select value={form.componentId} onChange={(e) => set({ componentId: e.target.value })}>
              <option value="">A new work</option>
              {mine.map((c) => (
                <option key={c.id} value={c.id}>
                  New version of {contentLabel(c)} (now v{c.latestVersion})
                </option>
              ))}
            </Select>
          </Field>
        )}
        {!target && (
          <Field label="Name" hint="Lower-case letters, digits and hyphens: how the work is referred to.">
            <Input value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="q3-sales-chart" autoFocus />
          </Field>
        )}
        <Field label="Display name" hint={target ? "Left empty, the work keeps its own." : undefined}>
          <Input value={form.displayName} onChange={(e) => set({ displayName: e.target.value })} placeholder={target ? contentLabel(target) : "Q3 sales chart"} />
        </Field>
        <Field label="Description">
          <Textarea value={form.description} onChange={(e) => set({ description: e.target.value })} rows={2} maxLength={500} placeholder={target?.description || "What it is, in a sentence or two"} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Category">
            <Select value={form.category} onChange={(e) => set({ category: e.target.value as WorkCategory | "" })}>
              <option value="">{target ? "Keep its category" : "No category"}</option>
              {WorkCategoryValues.map((c) => (
                <option key={c} value={c}>
                  {WORK_CATEGORY_LABELS[c]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Tags" hint="Separated by commas.">
            <Input value={form.tags} onChange={(e) => set({ tags: e.target.value })} placeholder="chart, sales" />
          </Field>
        </div>
        <CoverSection cover={cover} canDraw={canDraw} drawing={drawing} disabled={loading || busy} onDraw={() => void drawCover()} onRemove={() => setCover("")} />
        <RecipeSection capture={capture} form={form} set={set} />
        {(failure || (problem && form.name.trim())) && <ErrorBox>{failure || problem}</ErrorBox>}
      </div>
    </Dialog>
  );
}

/** CoverSection is the card's picture: without a chosen one, the work's
 *  own (a cover file beside it, an image's picture, a document's first
 *  page); "Make a cover with AI" draws one from the title and description,
 *  which the person keeps, redraws or removes (work covers PRD §2.2). */
function CoverSection({
  cover,
  canDraw,
  drawing,
  disabled,
  onDraw,
  onRemove,
}: {
  readonly cover: string;
  readonly canDraw: boolean;
  readonly drawing: boolean;
  readonly disabled: boolean;
  readonly onDraw: () => void;
  readonly onRemove: () => void;
}) {
  return (
    <section aria-label="Cover" className="flex items-center gap-3 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] px-3 py-2">
      {cover ? (
        <img src={cover} alt="The cover" className="h-16 w-28 shrink-0 rounded-[var(--radius)] object-cover" />
      ) : null}
      <p className="min-w-0 flex-1 text-xs text-[var(--color-fg-muted)]">
        {cover
          ? "The card's cover. Publishing keeps it."
          : "The card shows the work's own cover: one saved beside it, an image's own picture, or a document's first page."}
      </p>
      {cover && (
        <Button variant="ghost" size="sm" onClick={onRemove} disabled={disabled || drawing}>
          Remove
        </Button>
      )}
      {canDraw && (
        <AssistButton variant="text" label={cover ? "Make another" : "Make a cover with AI"} state={drawing ? "waiting" : "idle"} onRun={onDraw} disabled={disabled} />
      )}
    </section>
  );
}

/** RecipeSection is the captured recipe under the author's control: keep
 *  it or not, its prompt (a prompt can hold internal text: they see and
 *  edit it first), and each reference file ticked or not. */
function RecipeSection({
  capture,
  form,
  set,
}: {
  readonly capture: WorkRecipeCapture | null;
  readonly form: PublishWorkForm;
  readonly set: (patch: Partial<PublishWorkForm>) => void;
}) {
  if (capture === null) {
    return (
      <p className="text-sm text-[var(--color-fg-subtle)]" role="status">
        Reading how it was made…
      </p>
    );
  }
  const recipe = capture.recipe;
  if (!recipe) {
    return <p className="text-sm text-[var(--color-fg-subtle)]">No recipe: no agent's request made this version (an upload, or a person's edit).</p>;
  }
  const chips = recipeChips(recipe);
  const refs = recipe.references ?? [];
  return (
    <section aria-label="Recipe" className="flex flex-col gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] px-3 py-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm font-medium text-[var(--color-fg)]">Recipe</p>
          <p className="text-xs text-[var(--color-fg-subtle)]">How it was made, for others to make something like it. Check the prompt before you publish it.</p>
        </div>
        <Switch checked={form.includeRecipe} onChange={(on) => set({ includeRecipe: on })} ariaLabel="Include the recipe" label={form.includeRecipe ? "Included" : "Left out"} />
      </div>
      {form.includeRecipe && (
        <>
          {chips.length > 0 && (
            <ul className="flex flex-wrap gap-1.5" aria-label="Expert and skills">
              {chips.map((c) => (
                <li key={`${c.kind}:${c.label}`} className="rounded-full border border-[var(--color-border)] bg-[var(--color-surface-2)] px-2.5 py-0.5 text-xs text-[var(--color-fg-muted)]">
                  {c.label}
                </li>
              ))}
            </ul>
          )}
          <Field label="Prompt" hint="The request that made it. Edit or clear anything that should not be shared.">
            <Textarea value={form.prompt} onChange={(e) => set({ prompt: e.target.value })} rows={4} placeholder="No prompt" />
          </Field>
          {refs.length > 0 && (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1 text-xs font-medium text-[var(--color-fg-muted)]">Reference files</legend>
              {refs.map((r) => {
                const id = r.attachmentId ?? r.name;
                const on = !form.unticked.includes(id);
                return (
                  <label key={id} className="flex min-w-0 items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={on}
                      onChange={(e) => set({ unticked: e.target.checked ? form.unticked.filter((u) => u !== id) : [...form.unticked, id] })}
                    />
                    <span className="min-w-0 truncate">{r.name}</span>
                    <span className="shrink-0 text-xs text-[var(--color-fg-subtle)]">{formatBytes(r.sizeBytes)}</span>
                  </label>
                );
              })}
            </fieldset>
          )}
          {(capture.skipped ?? []).length > 0 && (
            <p className="text-xs text-[var(--color-fg-subtle)]">Left out (over the size or count limit): {(capture.skipped ?? []).join(", ")}.</p>
          )}
        </>
      )}
    </section>
  );
}
