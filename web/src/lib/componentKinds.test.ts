import { COMPONENT_KINDS, componentKindLabel, componentKindOfPackage } from "./componentKinds.ts";
import { CONTENT_KINDS, CONTENT_PACKAGE_KINDS, contentKindWord } from "./contentComponents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// Every kind round-trips through its package kind and has its own label (a
// sandbox or a previewer never reads "Auth provider").
for (const [kind, meta] of Object.entries(COMPONENT_KINDS)) {
  assert(componentKindOfPackage(meta.packageKind) === kind, `${kind} round trip`);
  assert(componentKindLabel(kind) === meta.label && componentKindLabel(meta.packageKind) === meta.label, `${kind} label`);
}
assert(componentKindLabel("sandbox") === "Sandbox" && componentKindLabel("previewer") === "Previewer", "the kinds the old chains mislabelled");
assert(componentKindLabel("bnbloc.unknown.package/v1") === "bnbloc.unknown.package/v1", "an unknown kind shows as itself");
for (const [kind, meta] of Object.entries(CONTENT_KINDS)) {
  assert(CONTENT_PACKAGE_KINDS[meta.packageKind] === kind, `${kind} package kind`);
}
assert(contentKindWord("prompt") === "prompt pack", "the prompt pack has its word");
