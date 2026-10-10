// The web build's identity, injected at build time (see vite.config.ts).
export const BUILD_VERSION: string = __BNBLOC_VERSION__;
export const BUILD_COMMIT: string = __BNBLOC_COMMIT__;

// buildLabel is the short form ("v0.1.8"); buildTitle adds the commit for a
// tooltip ("bnbloc v0.1.8 (abc1234)").
export function buildLabel(): string {
  return BUILD_VERSION === "dev" ? "dev" : `v${BUILD_VERSION}`;
}

export function buildTitle(desktopVersion?: string | null): string {
  const web = `bnbloc ${buildLabel()}${BUILD_COMMIT ? ` (${BUILD_COMMIT})` : ""}`;
  return desktopVersion && desktopVersion !== BUILD_VERSION ? `${web} · desktop v${desktopVersion}` : web;
}
