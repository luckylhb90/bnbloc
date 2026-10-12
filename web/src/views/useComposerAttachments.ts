import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type ChatAttachment } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { MaxAttachmentBytes } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";
import type { AttachmentChipItem, AttachmentChipStatus } from "@/views/AttachmentChips";
import { errorText } from "@/lib/errors";

// Raster images only: SVG (active content) is refused by the gateway (415).
export const ATTACHMENT_ACCEPT = [
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "application/pdf",
  "text/plain",
  "text/markdown",
  "application/zip",
  "application/json",
  ".csv",
  ".doc",
  ".docx",
  ".xls",
  ".xlsx",
  ".ppt",
  ".pptx",
].join(",");

interface PendingAttachment {
  readonly id: string;
  readonly file: File;
  readonly previewUrl?: string;
  readonly status: AttachmentChipStatus;
  readonly stored?: ChatAttachment;
  readonly error?: string;
}

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
      setFiles((cur) =>
        cur.map((file) =>
          file.id === pending.id
            ? { ...file, status: "uploading", error: undefined }
            : file,
        ),
      );
      try {
        const stored = await api.uploadAttachment(roomId, pending.file);
        uploaded.push(stored);
        setFiles((cur) =>
          cur.map((file) =>
            file.id === pending.id ? { ...file, status: "uploaded", stored } : file,
          ),
        );
      } catch (e) {
        const status = e instanceof ApiError ? e.status : 0;
        const message =
          status === 415
            ? "This file type can't be attached"
            : status === 413
              ? `${pending.file.name} is larger than ${formatBytes(MaxAttachmentBytes)}`
              : errorText(e, `Upload failed for ${pending.file.name}`);
        setFiles((cur) =>
          cur.map((file) =>
            file.id === pending.id ? { ...file, status: "error", error: message } : file,
          ),
        );
        toast.error(
          status === 415
            ? `${pending.file.name} can't be attached: SVG files and files whose content doesn't match their type are not allowed`
            : message,
        );
        return null;
      }
    }
    return uploaded;
  };

  const chipItems = useMemo<AttachmentChipItem[]>(
    () =>
      files.map((file) => ({
        id: file.id,
        filename: file.file.name,
        contentType: file.file.type,
        size: file.file.size,
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
    removeFile,
    clearFiles,
    uploadFiles,
    isUploading: files.some((file) => file.status === "uploading"),
  };
}
