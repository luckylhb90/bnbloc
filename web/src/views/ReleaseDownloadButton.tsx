import { useState } from "react";
import { DownloadSimpleIcon } from "@phosphor-icons/react";
import { api, type ReleaseFile } from "@/lib/api";
import { afterReleaseDownload } from "@/lib/macInstall";
import { Button, type ButtonProps } from "@/ui/Button";
import { errorText } from "@/lib/errors";

export function ReleaseDownloadButton({
  file,
  label = "Download",
  variant = "primary",
  size = "sm",
  className,
}: {
  readonly file: ReleaseFile;
  readonly label?: string;
  readonly variant?: ButtonProps["variant"];
  readonly size?: ButtonProps["size"];
  readonly className?: string;
}) {
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const download = async () => {
    setDownloading(true);
    setError(null);
    try {
      await api.downloadReleaseFile(file);
      // A macOS installer opens the install guide (Gatekeeper blocks the
      // not-yet-notarized app's first launch).
      afterReleaseDownload(file);
    } catch (failure) {
      setError(errorText(failure, "Could not download this file"));
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className={className}>
      <Button variant={variant} size={size} onClick={() => void download()} disabled={downloading}>
        <DownloadSimpleIcon size={14} />
        {downloading ? "Preparing download…" : label}
      </Button>
      {error && <p className="mt-2 text-xs text-[var(--color-danger)]" role="alert">{error}</p>}
    </div>
  );
}
