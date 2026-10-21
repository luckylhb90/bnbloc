import { useEffect, useState } from "react";
import type { ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { promptChoices, starterChoices, type PromptChoice } from "@/lib/promptPacks";
import { useContentChanged } from "@/lib/useContentChanged";

// The prompts a room offers its members: the choices (their packs, the
// room's, the Library's) and an empty room's starters. One loader for the
// composer and the selection action, reloaded when packs change.

// libraryPromptPacks are the prompt packs of the member's Library (their
// own, the org's and the platform's published ones, the default ones
// among them):
// loaded once per page and again when packs change (lib/contentChanges.ts);
// a failed load is retried.
let libraryPacks: Promise<readonly ContentComponent[]> | null = null;
function libraryPromptPacks(): Promise<readonly ContentComponent[]> {
  libraryPacks ??= contentApi
    .list("prompt")
    .then((r) => r.components ?? [])
    .catch(() => {
      libraryPacks = null;
      return [];
    });
  return libraryPacks;
}

type Loaded = { readonly choices: readonly PromptChoice[]; readonly starters: readonly PromptChoice[] };

// Before the first load, and when it fails: no prompts (the composer works
// without them).
const NONE: readonly PromptChoice[] = [];
const EMPTY: Loaded = { choices: NONE, starters: NONE };

// The last load per room: a new user of the same room (the selection
// action beside the composer) starts from it while it reloads, so its
// suggestions show at once.
const lastLoaded = new Map<string, Loaded>();

export function usePromptChoices(roomId: string, enabled = true): Loaded {
  const [state, setState] = useState<Loaded>(() => lastLoaded.get(roomId) ?? EMPTY);
  const [epoch, setEpoch] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    Promise.all([contentApi.uses(roomId), libraryPromptPacks()])
      .then(([r, library]) => {
        const next = { choices: promptChoices(r.member ?? [], r.room ?? [], library), starters: starterChoices(r.room ?? [], library) };
        lastLoaded.set(roomId, next);
        if (live) setState(next);
      })
      .catch(() => {
        // prompts are optional: the composer works without them
        if (live) setState(EMPTY);
      });
    return () => {
      live = false;
    };
  }, [roomId, enabled, epoch]);
  // A prompt pack saved, deleted or turned on or off (a use names no kind).
  useContentChanged(["prompt"], () => {
    libraryPacks = null;
    setEpoch((n) => n + 1);
  });
  return state;
}
