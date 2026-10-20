// MCP tool components (docs/architecture/content-components.md, "MCP tool
// components"): platform-published tool servers experts and skill packs
// require by name. The member catalog (GET /v1/components?kind=mcp) lists
// the ones visible to the member with their settings declaration and which
// platform and organization settings are set; a member's own settings live
// on their desktop (the Components page, never sent to the server). Pure
// helpers; the calls are in mcpToolsApi.ts and lib/desktop.ts.
import type { MemberComponent } from "./apiTypes.ts";
import type { ContentComponent } from "./contentComponents.ts";
import { ofScope, settingLabels, type DesktopComponentSettings } from "./componentSettings.ts";

// signsIn reports a tool each member signs in to (OAuth) instead of
// entering a credential.
export function signsIn(tool: MemberComponent): boolean {
  return tool.mcp?.http?.auth.scheme === "oauth";
}

// signInLine is the Components page' status of an OAuth tool on this
// computer.
export function signInLine(t: Pick<DesktopComponentSettings, "signIn">): string {
  const s = t.signIn;
  if (!s) return "Not signed in. Sign in to use it in your runs.";
  if (!s.valid) return `Your sign-in${s.server ? ` with ${s.server}` : ""} expired. Sign in again.`;
  return `Signed in${s.server ? ` with ${s.server}` : ""}.`;
}

export type CredentialState = "ready" | "missing" | "elsewhere";

export interface CredentialStatus {
  readonly state: CredentialState;
  readonly text: string;
}

// credentialStatus says whether a tool is set up for the member: its
// sign-in (OAuth), the platform's and organization's settings (the
// catalog's settingsMissing) and the member's own (local: the desktop's
// report; undefined in a browser).
export function credentialStatus(tool: MemberComponent, local?: DesktopComponentSettings): CredentialStatus {
  if (signsIn(tool)) {
    if (!local) return { state: "elsewhere", text: "Your own sign-in: sign in from the desktop app (the Components page)" };
    if (!local.signIn?.valid) return { state: "missing", text: "Needs setup: sign in from the Components page" };
  }
  const settings = tool.settings ?? [];
  const admin = tool.settingsMissing ?? [];
  if (admin.length > 0) return { state: "missing", text: `Needs setup: an admin has not set ${settingLabels(admin, settings).join(", ")}` };
  const own = ofScope(settings, "member").filter((s) => s.required && !s.default);
  if (!local) {
    if (own.length > 0) return { state: "elsewhere", text: "Your own settings: set them in the desktop app (the Components page)" };
    return { state: "ready", text: "Ready" };
  }
  const mine = local.missing.filter((k) => own.some((s) => s.key === k));
  if (mine.length > 0) return { state: "missing", text: `Needs setup: set ${settingLabels(mine, settings).join(", ")} on the Components page` };
  return { state: "ready", text: signsIn(tool) ? "Signed in on this computer" : "Ready" };
}

// toolUsers returns the content components (experts, skill packs) that
// require a tool, by name.
export function toolUsers(tool: string, content: readonly ContentComponent[]): ContentComponent[] {
  return content.filter((c) => (c.requires.mcp ?? []).some((r) => r.name === tool));
}
