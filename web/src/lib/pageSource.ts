import type { ComponentPageInfo } from "./wire.gen.ts";

// pageSource names a page's service with its installed version and, when
// the service says, the build it runs: "connections 0.2.0 · running v0.6.0".
export function pageSource(p: Pick<ComponentPageInfo, "component" | "version" | "serviceVersion">): string {
  let out = p.version ? `${p.component} ${p.version}` : p.component;
  if (p.serviceVersion && p.serviceVersion !== p.version) out += ` · running ${p.serviceVersion}`;
  return out;
}
