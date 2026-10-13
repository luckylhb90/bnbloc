import { apiUrl } from "@/lib/config";
import type { ConvertFileSrc, SiteFrameEnv } from "@/lib/siteFrame";

/** siteFrameEnv is this page's lib/siteFrame environment: the desktop
 *  shell's convertFileSrc (absent in a browser), the gateway base and the
 *  document's base URI. Every site frame (a member's site viewer, an app's
 *  demo) builds its src with it. */
export function siteFrameEnv(): SiteFrameEnv {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return { convertFileSrc: internals?.convertFileSrc, apiUrl, baseURI: document.baseURI };
}
