import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type ChatAttachment } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { AttachmentTypes, MaxAttachmentBytes } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";
import type { AttachmentChipItem, AttachmentChipStatus } from "@/views/AttachmentChips";
import { errorText } from "@/lib/errors";

// The files the picker offers: wire.AttachmentTypes, the list the upload
// holds every file to (by extension, then by content).
export const ATTACHMENT_ACCEPT = AttachmentTypes.flatMap((t) => [t.mime, ...t.extensions]).join(",");

// A pending attachment: a file the member picked (uploaded on send), or one
// already stored in the room that a draft brought along (a "Make similar"
// draft's references: stored, nothing to upload).
type PendingAttachment = {
  readonly id: string;
  readonly previewUrl?: string;
  readonly status: AttachmentChipStatus;
  readonly error?: string;
} & ({ readonly file: File; readonly stored?: ChatAttachment } | { readonly file?: undefined; readonly stored: ChatAttachment });

export function useComposerAttachments(roomId: string) {
  const [files, setFiles] = useState<PendingAttachment[]>([]);
  const previewUrlsRef = useRef<Set<string>>(new Set());
  const toast = useToast();

  useEffect(() => {
    return () => {
      previewUrlsRef.current.forEach((url) => URL.revokeObjectURL(url));
      previewUrlsRef.current.clear();
    };
  }, []);

  const addFiles = (selected: readonly File[]) => {
    const accepted: PendingAttachment[] = [];
    for (const file of selected) {
      if (file.size > MaxAttachmentBytes) {
        toast.error(`${file.name} is larger than ${formatBytes(MaxAttachmentBytes)}`);
        continue;
      }
      const previewUrl = file.type.startsWith("image/")
        ? URL.createObjectURL(file)
        : undefined;
      if (previewUrl) previewUrlsRef.current.add(previewUrl);
      accepted.push({
        id: crypto.randomUUID(),
        file,
        previewUrl,
        status: "ready",
      });
    }
    if (accepted.length > 0) setFiles((cur) => [...cur, ...accepted]);
  };

  // addStored brings attachments already stored in the room (a draft's
  // references) into the composer, as if uploaded.
  const addStored = useCallback((stored: readonly ChatAttachment[]) => {
    if (stored.length === 0) return;
    setFiles((cur) => [
      ...cur,
      ...stored.filter((s) => !cur.some((f) => f.stored?.id === s.id)).map((s) => ({ id: crypto.randomUUID(), stored: s, status: "uploaded" as const })),
    ]);
  }, []);

  const removeFile = (id: string) => {
    setFiles((cur) => {
      const removing = cur.find((file) => file.id === id);
      if (removing?.previewUrl) {
        URL.revokeObjectURL(removing.previewUrl);
        previewUrlsRef.current.delete(removing.previewUrl);
      }
      return cur.filter((file) => file.id !== id);
    });
  };

  const clearFiles = (sent: readonly PendingAttachment[]) => {
    for (const file of sent) {
      if (file.previewUrl) {
        URL.revokeObjectURL(file.previewUrl);
        previewUrlsRef.current.delete(file.previewUrl);
      }
    }
    setFiles([]);
  };

  const uploadFiles = async (): Promise<ChatAttachment[] | null> => {
    const uploaded: ChatAttachment[] = [];
    for (const pending of files) {
      if (pending.stored) {
        uploaded.push(pending.stored);
        continue;
      }
      if (!pending.file) continue;
      const file = pending.file;
      setFiles((cur) =>
        cur.map((file) =>
          file.id === pending.id
            ? { ...file, status: "uploading", error: undefined }
            : file,
        ),
      );
      try {
        const stored = await api.uploadAttachment(roomId, file);
        uploaded.push(stored);
        setFiles((cur) =>
          cur.map((file) =>
            file.id === pending.id ? { ...file, status: "uploaded", stored } : file,
          ),
        );
      } catch (e) {
        const status = e instanceof ApiError ? e.status : 0;
        // A refused type: the server's reason names the file and why.
        const message =
          status === 415
            ? errorText(e, `${file.name} can't be attached`)
            : status === 413
              ? `${file.name} is larger than ${formatBytes(MaxAttachmentBytes)}`
              : errorText(e, `Upload failed for ${file.name}`);
        setFiles((cur) =>
          cur.map((file) =>
            file.id === pending.id ? { ...file, status: "error", error: message } : file,
          ),
        );
        toast.error(message);
        return null;
      }
    }
    return uploaded;
  };

  const chipItems = useMemo<AttachmentChipItem[]>(
    () =>
      files.map((file) => ({
        id: file.id,
        filename: file.file?.name ?? file.stored?.filename ?? "",
        contentType: file.file?.type ?? file.stored?.contentType ?? "",
        size: file.file?.size ?? file.stored?.size ?? 0,
        previewUrl: file.previewUrl,
        status: file.status,
        error: file.error,
      })),
    [files],
  );

  return {
    files,
    chipItems,
    addFiles,
    addStored,
    removeFile,
    clearFiles,
    uploadFiles,
    isUploading: files.some((file) => file.status === "uploading"),
  };
}
