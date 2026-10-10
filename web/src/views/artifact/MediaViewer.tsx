import { useEffect, useState } from "react";
import type { ArtifactViewerProps } from "./registry";

// MediaViewer: the platform's own audio/video player on a blob URL (the
// bytes were fetched with the session; nothing streams from a credentialed
// URL). Formats the webview cannot decode report it instead of a dead player.

export default function MediaViewer({ blob, file }: ArtifactViewerProps) {
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const next = URL.createObjectURL(blob);
    setUrl(next);
    setFailed(false);
    return () => URL.revokeObjectURL(next);
  }, [blob]);
  if (!url) return null;
  const audio = file.mimeType.startsWith("audio/");
  return (
    <div className="grid h-full place-items-center bg-[var(--color-bg)] p-4">
      {failed ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">This format cannot be played here. Download it to open it locally.</p>
      ) : audio ? (
        <audio controls src={url} onError={() => setFailed(true)} className="w-full max-w-xl" aria-label={file.name} />
      ) : (
        <video controls playsInline src={url} onError={() => setFailed(true)} className="max-h-full max-w-full rounded-[var(--radius)]" aria-label={file.name} />
      )}
    </div>
  );
}
