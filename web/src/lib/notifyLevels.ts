// A member's notification level for a channel or thread (channels PRD C4):
// what they set ("" the default) and the effective level the room service
// resolves (wire.EffectiveNotifyLevel, the one rule; never recomputed here).

import type { NotifyLevel } from "./wire.gen.ts";

export type { NotifyLevel };

const NAMES: Record<Exclude<NotifyLevel, "">, string> = {
  all: "All messages",
  mentions: "Mentions only",
  muted: "Muted",
};

/** The choices of a channel's Notifications menu, the default first,
 *  named with what it means for this member now. */
export function notifyChoices(effective: NotifyLevel | undefined): readonly { readonly level: NotifyLevel; readonly label: string }[] {
  const now = effective && effective !== "muted" ? NAMES[effective].toLowerCase() : "mentions only";
  return [
    { level: "", label: `Default (${now})` },
    { level: "all", label: NAMES.all },
    { level: "mentions", label: NAMES.mentions },
    { level: "muted", label: NAMES.muted },
  ];
}

/** What a level change says once it is saved. */
export function notifySaved(name: string, level: NotifyLevel): string {
  switch (level) {
    case "all":
      return `You will be told of every message in #${name}`;
    case "mentions":
      return `You will be told only when mentioned in #${name}`;
    case "muted":
      return `#${name} is muted: a direct mention still reaches you`;
  }
  return `#${name} is back to the default`;
}
