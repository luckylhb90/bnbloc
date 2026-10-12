import { useCallback, useEffect, useState, type Dispatch, type KeyboardEvent, type RefObject, type SetStateAction } from "react";
import { contentApi } from "@/lib/contentComponentsApi";
import {
  choiceFromRef,
  fillPrompt,
  insertPrompt,
  promptChoices,
  roomStarters,
  slashMatches,
  slashQuery,
  type PromptChoice,
  type PromptRef,
} from "@/lib/promptPacks";
import { PROMPT_PACKS_CHANGED, promptPacksApi } from "@/lib/promptPacksApi";

export interface ComposerPrompts {
  // The prompts of the member's packs and the room's.
  readonly choices: readonly PromptChoice[];
  // The starter prompts of the room's packs (an empty room's examples).
  readonly starters: readonly PromptChoice[];
  // The /command menu: its matches (empty when closed) and highlight.
  readonly slashMatches: readonly PromptChoice[];
  readonly active: number;
  readonly setActive: (i: number) => void;
  // detect follows the draft: a message that starts with "/word" opens
  // the menu.
  readonly detect: (value: string, caret: number) => void;
  // onKeyDown drives the open menu (arrows, Enter/Tab pick, Escape
  // closes); true when it handled the key.
  readonly onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  readonly pickSlash: (i: number) => void;
  // pick inserts a prompt (from the picker, or an expert's quick prompt):
  // one with variables asks for them first.
  readonly pick: (c: PromptChoice) => void;
  readonly pickRef: (ref: PromptRef) => void;
  // The prompt whose variables the form asks for, and its answers.
  readonly filling: PromptChoice | null;
  readonly cancelFill: () => void;
  readonly submitFill: (values: Readonly<Record<string, string>>) => void;
}

// useComposerPrompts loads the prompts a room's composer offers and inserts
// them into the draft (counting each insertion once). Disabled (the people
// chat) it offers none.
export function useComposerPrompts(
  roomId: string,
  enabled: boolean,
  setText: Dispatch<SetStateAction<string>>,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): ComposerPrompts {
  const [choices, setChoices] = useState<readonly PromptChoice[]>([]);
  const [starters, setStarters] = useState<readonly PromptChoice[]>([]);
  const [slash, setSlash] = useState<{ query: string; end: number } | null>(null);
  const [active, setActive] = useState(0);
  const [filling, setFilling] = useState<{ choice: PromptChoice; slashEnd: number | null } | null>(null);
  const [epoch, setEpoch] = useState(0);

  useEffect(() => {
    if (!enabled) return;
    let live = true;
    contentApi
      .uses(roomId)
      .then((r) => {
        if (!live) return;
        setChoices(promptChoices(r.member ?? [], r.room ?? []));
        setStarters(roomStarters(r.room ?? []));
      })
      .catch(() => {
        // prompts are optional: the composer works without them
        if (!live) return;
        setChoices([]);
        setStarters([]);
      });
    return () => {
      live = false;
    };
  }, [roomId, enabled, epoch]);
  useEffect(() => {
    const reload = () => setEpoch((n) => n + 1);
    window.addEventListener(PROMPT_PACKS_CHANGED, reload);
    return () => window.removeEventListener(PROMPT_PACKS_CHANGED, reload);
  }, []);

  const matches = enabled && slash ? slashMatches(choices, slash.query) : [];
  const highlighted = matches.length > 0 ? Math.min(active, matches.length - 1) : -1;

  const detect = useCallback(
    (value: string, caret: number) => {
      const q = enabled ? slashQuery(value.slice(0, caret)) : null;
      setSlash(q === null ? null : { query: q, end: caret });
      setActive(0);
    },
    [enabled],
  );

  const insert = useCallback(
    (c: PromptChoice, values: Readonly<Record<string, string>>, slashEnd: number | null) => {
      const text = fillPrompt(c.prompt.body, values);
      setText((cur) => insertPrompt(cur, text, slashEnd));
      setSlash(null);
      // The count is best-effort: inserting never waits on it.
      void promptPacksApi.recordUse(c.componentId, c.prompt.id, { roomId, version: c.version }).catch(() => {});
      window.requestAnimationFrame(() => {
        const el = textareaRef.current;
        if (!el) return;
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      });
    },
    [roomId, setText, textareaRef],
  );

  const start = useCallback(
    (c: PromptChoice, slashEnd: number | null) => {
      if ((c.prompt.variables?.length ?? 0) > 0) {
        setSlash(null);
        setFilling({ choice: c, slashEnd });
      } else insert(c, {}, slashEnd);
    },
    [insert],
  );

  const pickSlash = (i: number) => {
    const c = matches[i];
    if (c && slash) start(c, slash.end);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (matches.length === 0) return false;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setActive((highlighted + (e.key === "ArrowDown" ? 1 : -1) + matches.length) % matches.length);
      return true;
    }
    if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault();
      pickSlash(Math.max(0, highlighted));
      return true;
    }
    if (e.key === "Escape") {
      setSlash(null);
      return true;
    }
    return false;
  };

  return {
    choices,
    starters,
    slashMatches: matches,
    active: highlighted,
    setActive,
    detect,
    onKeyDown,
    pickSlash,
    pick: (c) => start(c, null),
    pickRef: (ref) => start(choiceFromRef(ref), null),
    filling: filling?.choice ?? null,
    cancelFill: () => setFilling(null),
    submitFill: (values) => {
      if (!filling) return;
      insert(filling.choice, values, filling.slashEnd);
      setFilling(null);
    },
  };
}
