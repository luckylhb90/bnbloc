import { useEffect, useState } from "react";
import { CheckIcon, FloppyDiskIcon } from "@phosphor-icons/react";
import { gatewayBase } from "@/lib/config";
import { errorText } from "@/lib/errors";
import { localFiles, type LocalSaveMode } from "@/lib/localFiles";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";

// LocalSaveChip saves the shown version back to the local document the
// room file came from (desktop only; docs/prd/2026-10-14-ai-document-editing.md
// §4 rule 5). The shell writes only over the bytes it last synced with the
// file; a file changed (or gone) since is the member's choice: a copy
// beside it (the default) or replacing it.

export function LocalSaveChip({
  artifactId,
  version,
  load,
  className,
}: {
  readonly artifactId: string;
  readonly version: number;
  /** The shown version's bytes. */
  readonly load: () => Promise<Blob>;
  readonly className: string;
}) {
  const toast = useToast();
  const [local, setLocal] = useState<{ token: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState<"changed" | "missing" | null>(null);
  // What the last save of this tab did (the chip says it until another
  // version is shown: a toast alone is easy to miss).
  const [saved, setSaved] = useState<{ version: number; text: string } | null>(null);

  useEffect(() => {
    if (!localFiles.available()) return;
    let live = true;
    localFiles.forArtifact(gatewayBase(), artifactId).then(
      (l) => live && setLocal(l),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [artifactId]);

  if (!local) return null;

  const save = async (mode: LocalSaveMode) => {
    setBusy(true);
    try {
      const bytes = await (await load()).arrayBuffer();
      const result = await localFiles.save(local.token, bytes, version, mode);
      if (result.status === "written" || result.status === "copied") {
        const text = result.status === "written" ? `Saved v${version} to ${local.name}` : `Saved v${version} as ${result.name}`;
        setConflict(null);
        setSaved({ version, text });
        toast.success(text);
      } else {
        setConflict(result.status);
      }
    } catch (e) {
      toast.error(errorText(e, `Could not save to ${local.name}`));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button
        type="button"
        className={className}
        disabled={busy}
        onClick={() => void save("check")}
        title={`Write v${version} to ${local.name} on this computer`}
      >
        {saved?.version === version ? (
          <>
            <CheckIcon size={14} className="text-[var(--color-accent)]" /> {saved.text}
          </>
        ) : (
          <>
            <FloppyDiskIcon size={14} /> {busy ? "Saving…" : `Save to ${local.name}`}
          </>
        )}
      </button>
      <Dialog
        open={conflict !== null}
        size="sm"
        onClose={() => setConflict(null)}
        title={conflict === "missing" ? `${local.name} is no longer where it was` : `${local.name} changed on this computer`}
        description={
          conflict === "missing"
            ? "It was moved or deleted since you opened it. Save this version as a copy in its folder instead?"
            : "It was edited since you opened it or last saved to it. Save this version as a copy beside it, or replace it and lose those edits."
        }
        footer={
          <>
            <Button variant="ghost" onClick={() => setConflict(null)}>
              Cancel
            </Button>
            {conflict === "changed" && (
              <Button variant="danger" disabled={busy} onClick={() => void save("overwrite")}>
                Replace it
              </Button>
            )}
            <Button variant="primary" disabled={busy} onClick={() => void save("copy")}>
              Save a copy
            </Button>
          </>
        }
      />
    </>
  );
}
