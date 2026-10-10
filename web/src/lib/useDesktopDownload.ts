import { useEffect, useState } from "react";
import { api, type ReleaseFile } from "@/lib/api";
import { gatewayBase } from "@/lib/config";
import { detectDesktopArch, detectDesktopPlatform, pickDesktopDownload, type DesktopArch } from "@/lib/desktopDownload";
import { isGatewayArtifactUrl } from "@/lib/desktopHandoff";
import { useQuery } from "@/lib/useQuery";

const RELEASES_QUERY_KEY = "app-releases";
const POLICY_QUERY_KEY = "app-update-policy";

// useDesktopDownload resolves the desktop installer for this browser from the
// Release Hub, restricted to the instance's update channel (the channel its
// desktop clients follow), never an updater-only archive.
export function useDesktopDownload(): {
  readonly url: string | null;
  readonly file: ReleaseFile | null;
  readonly loading: boolean;
  readonly isGatewayArtifact: boolean;
} {
  const releases = useQuery(RELEASES_QUERY_KEY, api.getAppReleases, { staleMs: Infinity });
  const policy = useQuery(POLICY_QUERY_KEY, api.getUpdatePolicy, { staleMs: Infinity });
  const [arch, setArch] = useState<DesktopArch | null>(null);
  useEffect(() => {
    let cancelled = false;
    void detectDesktopArch().then((detected) => {
      if (!cancelled) setArch(detected);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  const userAgent = typeof navigator === "undefined" ? "" : navigator.userAgent;
  // An unreadable policy leaves the channel unknown (no filter) rather than
  // hiding every download.
  const channel = policy.data?.channel ?? null;
  const policySettled = policy.data !== undefined || policy.error !== undefined;
  const file = releases.data && policySettled
    ? pickDesktopDownload(releases.data.releases, { platform: detectDesktopPlatform(userAgent), channel, arch })
    : null;
  return {
    url: file?.url ?? null,
    file,
    loading: (releases.data === undefined && releases.error === undefined) || !policySettled,
    isGatewayArtifact: file ? isGatewayArtifactUrl(file.url, gatewayBase(), window.location.origin) : false,
  };
}
