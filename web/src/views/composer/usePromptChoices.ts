import { useEffect, useState } from "react";
import type { ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { promptChoices, starterChoices, type PromptChoice } from "@/lib/promptPacks";
import { PROMPT_PACKS_CHANGED } from "@/lib/promptPacksApi";

// The prompts a room offers its members: the choices (their packs, the
// room's, the Library's) and an empty room's starters. One loader for the
// composer and the selection action, reloaded when packs change.

// libraryPromptPacks are the prompt packs of the member's Library (their
// own, the org's and the platform's published ones, the built-in ones):
// loaded once per page and again when packs change (PROMPT_PACKS_CHANGED);
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

export function usePromptChoices(roomId: string, enabled = true): { readonly choices: readonly PromptChoice[]; readonly starters: readonly PromptChoice[] } {
  const [state, setState] = useState<{ choices: readonly PromptChoice[]; starters: readonly PromptChoice[] }>({ choices: [], starters: [] });
  const [epoch, setEpoch] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    Promise.all([contentApi.uses(roomId), libraryPromptPacks()])
      .then(([r, library]) => {
        if (live) setState({ choices: promptChoices(r.member ?? [], r.room ?? [], library), starters: starterChoices(r.room ?? [], library) });
      })
      .catch(() => {
        // prompts are optional: the composer works without them
        if (live) setState({ choices: [], starters: [] });
      });
    return () => {
      live = false;
    };
  }, [roomId, enabled, epoch]);
  useEffect(() => {
    const reload = () => {
      libraryPacks = null;
      setEpoch((n) => n + 1);
    };
    window.addEventListener(PROMPT_PACKS_CHANGED, reload);
    return () => window.removeEventListener(PROMPT_PACKS_CHANGED, reload);
  }, []);
  return state;
}
