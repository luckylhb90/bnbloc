import { roomLinkAudience } from "./roomAccess.ts";

// Every pair of the two settings says something different, and says who can
// read and how they join.
const said = new Set<string>();
for (const visibility of ["shared", "private"] as const) {
  for (const previewable of [true, false]) {
    const text = roomLinkAudience(visibility, previewable);
    if (said.has(text)) throw new Error(`${visibility}/${previewable} repeats a sentence: ${text}`);
    said.add(text);
    if (previewable && !text.includes("read it")) throw new Error(`${visibility}/${previewable} does not say it can be read: ${text}`);
    if (!previewable && text.includes("and read it")) throw new Error(`${visibility}/${previewable} promises reading: ${text}`);
    if (visibility === "private" && !text.includes("invite code")) throw new Error(`${visibility}/${previewable} does not name the invite code: ${text}`);
  }
}
