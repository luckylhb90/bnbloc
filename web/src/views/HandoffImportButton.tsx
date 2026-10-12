import { useRef, useState } from "react";
import { PackageIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { importSummary } from "@/lib/handoff";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// HandoffImportButton picks a handoff package (.zip), imports it into a NEW
// room owned by the caller, and opens that room. The original members are not
// added: the imported room's banner offers them as invite suggestions.
export function HandoffImportButton({
  onImported,
  variant = "outline",
}: {
  readonly onImported: (roomId: string) => void;
  readonly variant?: "outline" | "primary" | "ghost";
}) {
  const input = useRef<HTMLInputElement | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const onFile = async (file: File | undefined) => {
    if (!file) return;
    setBusy(true);
    toast.info(`Importing ${file.name}…`);
    try {
      const res = await api.importRoomPackage(file);
      toast.success(importSummary(res));
      onImported(res.roomId);
    } catch (e) {
      toast.error(
        e instanceof ApiError && e.status === 413 ? "That handoff package is too large to import" : errorText(e, "Import failed"),
      );
    } finally {
      setBusy(false);
      if (input.current) input.current.value = "";
    }
  };

  return (
    <>
      <input
        ref={input}
        type="file"
        accept=".zip,application/zip"
        className="hidden"
        aria-hidden="true"
        tabIndex={-1}
        onChange={(e) => void onFile(e.target.files?.[0])}
      />
      <Button variant={variant} onClick={() => input.current?.click()} disabled={busy} title="Create a room from a handoff package">
        <PackageIcon size={16} />
        {busy ? "Importing…" : "Import handoff package"}
      </Button>
    </>
  );
}
