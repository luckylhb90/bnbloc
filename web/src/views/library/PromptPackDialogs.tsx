import { useEffect, useRef, useState } from "react";
import { api, type RoomSummary } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent, type ContentComponentDetail } from "@/lib/contentComponents";
import { apiErrorText, contentApi } from "@/lib/contentComponentsApi";
import { variableLabel, PROMPT_IMPORT_ACCEPT } from "@/lib/promptPacks";
import { announcePromptPacksChanged, promptPacksApi } from "@/lib/promptPacksApi";
import { lastRoom } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { ContentByline } from "@/views/library/ContentSharing";

// PromptPackViewDialog shows a pack's prompts at a version: title, /command,
// tags, variables, body and how often each was inserted. roomId reads a pack
// someone else turned on in that room.
export function PromptPackViewDialog({
  pack,
  roomId,
  onClose,
}: {
  readonly pack: ContentComponent | null;
  readonly roomId?: string;
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const [detail, setDetail] = useState<ContentComponentDetail | null>(null);
  const [version, setVersion] = useState("");
  const [shown, setShown] = useState<ContentComponent | null>(null);

  useEffect(() => {
    setDetail(null);
    setShown(pack);
    if (!pack) return;
    setVersion(pack.latestVersion);
    let live = true;
    contentApi
      .get(pack.id, roomId)
      .then((d) => live && setDetail(d))
      .catch((e) => live && toast.error(apiErrorText(e, "Could not load the pack")));
    return () => {
      live = false;
    };
  }, [pack, roomId, toast]);
  useEffect(() => {
    if (!pack || !version || version === pack.latestVersion) {
      setShown(pack);
      return;
    }
    let live = true;
    contentApi
      .version(pack.id, version, roomId)
      .then((v) => live && setShown(v.component))
      .catch((e) => live && toast.error(apiErrorText(e, "Could not load that version")));
    return () => {
      live = false;
    };
  }, [pack, version, roomId, toast]);

  const c = shown ?? pack;
  return (
    <Dialog open={!!pack} onClose={onClose} size="lg" title={c ? contentLabel(c) : undefined} description={c?.description || undefined}>
      {c && (
        <div className="flex flex-col gap-4 text-sm">
          <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-subtle)]">
            <Badge>Prompt pack</Badge>
            <span className="font-mono">{c.name}</span>
            <span>
              <ContentByline component={c} />
            </span>
            {detail && detail.versions.length > 1 ? (
              <Select value={version} onChange={(e) => setVersion(e.target.value)} className="h-8 w-36 text-xs" aria-label="Version">
                {detail.versions.map((v) => (
                  <option key={v.version} value={v.version}>
                    v{v.version}
                    {v.version === c.latestVersion ? " (latest)" : ""}
                  </option>
                ))}
              </Select>
            ) : (
              <span>v{version}</span>
            )}
          </div>
          <ul className="flex flex-col gap-3">
            {(c.prompts ?? []).map((p) => (
              <li key={p.id} className="flex flex-col gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{p.title}</span>
                  {p.slash && <span className="font-mono text-xs text-[var(--color-accent)]">{p.slash}</span>}
                  {(p.tags ?? []).map((t) => (
                    <Badge key={t}>#{t}</Badge>
                  ))}
                  <span className="ml-auto text-xs text-[var(--color-fg-subtle)]">{p.uses > 0 ? `used ${p.uses}×` : "not used yet"}</span>
                </div>
                <p className="whitespace-pre-wrap break-words text-[var(--color-fg-muted)]">{p.body}</p>
                {(p.variables?.length ?? 0) > 0 && (
                  <p className="text-xs text-[var(--color-fg-subtle)]">
                    Asks for:{" "}
                    {p.variables
                      ?.map((v) => `${variableLabel(v)}${v.options?.length ? ` (${v.options.join(", ")})` : v.default ? ` (default ${v.default})` : ""}`)
                      .join(", ")}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Dialog>
  );
}

// PromptImportDialog imports a markdown file (one prompt per heading), a
// json file of prompts, or an archive of them, as a new pack or a new
// version of one of the member's.
export function PromptImportDialog({
  open,
  mine,
  onClose,
  onImported,
}: {
  readonly open: boolean;
  readonly mine: readonly ContentComponent[];
  readonly onClose: () => void;
  readonly onImported: (c: ContentComponent) => void;
}) {
  const toast = useToast();
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [into, setInto] = useState("");
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (open) {
      setFile(null);
      setName("");
      setInto("");
    }
  }, [open]);
  const submit = async () => {
    if (!file || busy) return;
    setBusy(true);
    try {
      const d = await promptPacksApi.importFile(file, { name, existingId: into || undefined });
      toast.success(into ? `Imported version ${d.component.latestVersion}` : `Imported ${d.component.name} with ${d.component.prompts?.length ?? 0} prompts`);
      announcePromptPacksChanged();
      onImported(d.component);
      onClose();
    } catch (e) {
      toast.error(apiErrorText(e, "Could not import"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Import prompts"
      description="A markdown file with one prompt per heading (lines like slash: /name or tags: a, b under a heading set them), a json list of prompts, or a .zip of such files. {{name}} placeholders become variables."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!file || busy}>
            {busy ? "Importing…" : "Import"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="File">
          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => fileRef.current?.click()}>
              Choose a file
            </Button>
            <span className="min-w-0 truncate text-sm text-[var(--color-fg-muted)]">{file ? file.name : "No file chosen"}</span>
            <input ref={fileRef} type="file" accept={PROMPT_IMPORT_ACCEPT} className="hidden" onChange={(e) => setFile(e.currentTarget.files?.[0] ?? null)} />
          </div>
        </Field>
        <Field label="Import as">
          <Select value={into} onChange={(e) => setInto(e.target.value)} aria-label="Import as">
            <option value="">A new prompt pack</option>
            {mine.map((c) => (
              <option key={c.id} value={c.id}>
                A new version of {contentLabel(c)} (v{c.latestVersion})
              </option>
            ))}
          </Select>
        </Field>
        {!into && (
          <Field label="Name" hint="Optional: taken from the file when empty.">
            <Input value={name} placeholder="release-writing" onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
      </div>
    </Dialog>
  );
}

// AddToRoomDialog turns a pack on for a room the member picks (its owner or
// an admin may); its prompts then show in that room's composer for everyone.
export function AddToRoomDialog({ pack, onClose }: { readonly pack: ContentComponent | null; readonly onClose: () => void }) {
  const { subject } = useAuth();
  const toast = useToast();
  const [rooms, setRooms] = useState<RoomSummary[] | null>(null);
  const [roomId, setRoomId] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!pack || !subject) return;
    let live = true;
    setRooms(null);
    api
      .myRooms(subject)
      .then((r) => {
        if (!live) return;
        const list = r.rooms ?? [];
        setRooms(list);
        const last = lastRoom();
        setRoomId(list.some((x) => x.roomId === last) ? (last as string) : (list[0]?.roomId ?? ""));
      })
      .catch(() => live && setRooms([]));
    return () => {
      live = false;
    };
  }, [pack, subject]);
  const add = async () => {
    if (!pack || !roomId || busy) return;
    setBusy(true);
    try {
      await contentApi.putUse("room", pack.id, { roomId });
      toast.success(`${contentLabel(pack)} is on in the room`);
      announcePromptPacksChanged();
      onClose();
    } catch (e) {
      toast.error(apiErrorText(e, "Could not turn it on for the room"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!pack}
      onClose={onClose}
      size="sm"
      title="Turn on for a room"
      description={pack ? `Everyone in the room gets the prompts of ${contentLabel(pack)} in the composer. The room's owner and admins can do this.` : undefined}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void add()} disabled={!roomId || busy}>
            Turn on
          </Button>
        </>
      }
    >
      {rooms === null ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">Loading rooms…</p>
      ) : rooms.length === 0 ? (
        <p className="text-sm text-[var(--color-fg-muted)]">Join or create a room first.</p>
      ) : (
        <Field label="Room">
          <Select value={roomId} onChange={(e) => setRoomId(e.target.value)}>
            {rooms.map((r) => (
              <option key={r.roomId} value={r.roomId}>
                {r.name || r.roomId}
              </option>
            ))}
          </Select>
        </Field>
      )}
    </Dialog>
  );
}
