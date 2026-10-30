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

/** The choices of a channel's Notifications row, the default first,
 *  named with what it means for this member now. */
export function notifyChoices(effective: NotifyLevel | undefined): readonly { readonly value: NotifyLevel; readonly short: string; readonly label: string }[] {
  return [
    { value: "", short: "Default", label: `Default (${defaultMeaning(effective)})` },
    { value: "all", short: "All", label: NAMES.all },
    { value: "mentions", short: "Mentions", label: NAMES.mentions },
    { value: "muted", short: "Muted", label: NAMES.muted },
  ];
}

/** The caption under the row: what the member hears from the channel now. */
export function notifyCaption(level: NotifyLevel | undefined, effective: NotifyLevel | undefined): string {
  if (!level) return `Default here: ${defaultMeaning(effective)}`;
  return level === "muted" ? "Muted: only a direct mention" : `${NAMES[level]}`;
}

function defaultMeaning(effective: NotifyLevel | undefined): string {
  return effective && effective !== "muted" ? NAMES[effective].toLowerCase() : "mentions only";
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
