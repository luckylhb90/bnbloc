import { useCallback, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { saveBlob } from "@/lib/saveBlob";
import { useToast } from "@/ui/Toast";

// useHandoffExport downloads the room's handoff package (owner/admin), with
// the running byte count as progress and toasts for start/finish/failure.
export function useHandoffExport(roomId: string) {
  const toast = useToast();
  const [progressBytes, setProgressBytes] = useState<number | null>(null);
  const running = useRef(false);
  const run = useCallback(async () => {
    if (running.current) return;
    running.current = true;
    setProgressBytes(0);
    toast.info("Preparing handoff package…");
    try {
      const { blob, filename } = await api.exportRoomPackage(roomId, setProgressBytes);
      saveBlob(blob, filename);
      toast.success(`Handoff package downloaded (${formatBytes(blob.size)})`);
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "Export failed");
    } finally {
      running.current = false;
      setProgressBytes(null);
    }
  }, [roomId, toast]);
  return { exporting: progressBytes !== null, progressBytes: progressBytes ?? 0, run };
}
