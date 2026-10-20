import { apiUrl } from "@/lib/config";
import { themeFromBackground, type FrameEnv } from "@/lib/previewerFrame";
import type { ConvertFileSrc, SiteFrameEnv } from "@/lib/siteFrame";

/** siteFrameEnv is this page's lib/siteFrame environment: the desktop
 *  shell's convertFileSrc (absent in a browser), the gateway base and the
 *  document's base URI. Every site frame (a member's site viewer, an app's
 *  demo) builds its src with it. */
export function siteFrameEnv(): SiteFrameEnv {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return { convertFileSrc: internals?.convertFileSrc, apiUrl, baseURI: document.baseURI };
}

/** frameEnv is the same environment for a frame bundle (a previewer, a
 *  component's settings page), with the origin its messages are pinned to. */
export function frameEnv(): FrameEnv {
  return { ...siteFrameEnv(), hostOrigin: window.location.origin };
}

/** frameTheme is the app's theme, handed to a frame bundle at start. */
export function frameTheme(): "light" | "dark" {
  return themeFromBackground(getComputedStyle(document.documentElement).getPropertyValue("--color-bg"));
}
