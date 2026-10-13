import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type RefObject } from "react";
import { ENHANCE_PROMPT, isAbort, runTextAssist, useAssistTask } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import { useToast } from "@/ui/Toast";
import type { AssistRunState } from "@/views/AssistButton";

// useEnhancePrompt is the composer's "Enhance prompt" (AI assist PRD §5):
// the enhance-prompt task rewrites what the member typed, streamed into
// the composer in place of their text (read-only while it runs). Stop, or
// a failure, puts their text back; once done, Undo (or Cmd/Ctrl+Z before
// they edit it) restores it. Nothing is ever sent for them. Hidden (task
// null) when assists are unavailable.

export function useEnhancePrompt({
  roomId,
  channelId,
  expertId,
  text,
  setText,
  attachmentIds,
  taRef,
}: {
  readonly roomId: string;
  // The composer's channel (the recent messages the server reads) and the
  // expert picked for the message (else the server uses the room's).
  readonly channelId?: string | null;
  readonly expertId?: string | null;
  readonly text: string;
  readonly setText: (t: string) => void;
  // The attachments already stored in the room (the server reads their
  // names and types; a file not yet uploaded is not sent).
  readonly attachmentIds: readonly string[];
  readonly taRef: RefObject<HTMLTextAreaElement | null>;
}) {
  const toast = useToast();
  const task = useAssistTask(ENHANCE_PROMPT);
  const [state, setState] = useState<AssistRunState>("idle");
  // What the member wrote and what replaced it: Undo is offered while the
  // composer still holds the rewrite.
  const [undo, setUndo] = useState<{ original: string; result: string } | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(
    () => () => {
      abortRef.current?.abort();
    },
    [roomId],
  );

  const focusEnd = useCallback(() => {
    requestAnimationFrame(() => {
      const ta = taRef.current;
      if (!ta) return;
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    });
  }, [taRef]);

  const run = useCallback(async () => {
    const original = text;
    if (!task || state !== "idle" || !original.trim()) return;
    const ctl = new AbortController();
    abortRef.current = ctl;
    setUndo(null);
    setState("waiting");
    try {
      const { text: result } = await runTextAssist(
        task.name,
        {
          text: original,
          roomId,
          ...(channelId ? { channelId } : {}),
          ...(expertId ? { expertId } : {}),
          ...(attachmentIds.length > 0 ? { attachmentIds: [...attachmentIds] } : {}),
        },
        {
          signal: ctl.signal,
          onDelta: (soFar) => {
            setState("streaming");
            setText(soFar);
          },
        },
      );
      const rewritten = result.trim();
      if (!rewritten) {
        setText(original);
        toast.error("The assist gave back nothing; your text is unchanged");
      } else {
        setText(rewritten);
        setUndo({ original, result: rewritten });
      }
    } catch (e) {
      setText(original);
      if (!isAbort(e)) toast.error(errorText(e, "Could not enhance the prompt"));
    } finally {
      if (abortRef.current === ctl) abortRef.current = null;
      setState("idle");
      focusEnd();
    }
  }, [task, state, text, roomId, channelId, expertId, attachmentIds, setText, toast, focusEnd]);

  const stop = useCallback(() => abortRef.current?.abort(), []);

  const canUndo = !!undo && state === "idle" && text === undo.result;
  const restore = useCallback(() => {
    if (!undo) return;
    setText(undo.original);
    setUndo(null);
    focusEnd();
  }, [undo, setText, focusEnd]);

  /** Cmd/Ctrl+Z right after an enhance restores the member's text; true
   *  when it took the key. */
  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>) => {
      if (!canUndo || e.key.toLowerCase() !== "z" || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return false;
      e.preventDefault();
      restore();
      return true;
    },
    [canUndo, restore],
  );

  return { task, state, run, stop, canUndo, undo: restore, onKeyDown };
}
