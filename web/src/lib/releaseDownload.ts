import { isGatewayArtifactUrl } from "./desktopHandoff.ts";
import { saveBlob } from "./saveBlob.ts";
import { ApiError } from "./errors.ts";
import { authedFetch, errorDetail } from "./session.ts";

// downloadReleaseFile saves a release-hub file. A file hosted on this
// gateway's artifact store needs the session bearer, so it is fetched through
// the shared authenticated transport and saved from a blob; any other host
// gets a plain link and never sees the bearer.
export async function downloadReleaseFile(url: string, name: string, gatewayUrl: string): Promise<void> {
  if (!isGatewayArtifactUrl(url, gatewayUrl, window.location.origin)) {
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    link.rel = "noreferrer";
    link.target = "_blank";
    link.click();
    return;
  }

  const response = await authedFetch(url);
  if (!response.ok) throw new ApiError(response.status, await errorDetail(response));

  saveBlob(await response.blob(), name);
}
