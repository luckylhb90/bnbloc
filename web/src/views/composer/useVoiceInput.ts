import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { transcribeAudio } from "@/lib/media";
import { openVoiceRecorder, type VoiceState } from "@/lib/voice";

// useVoiceInput is the composer's microphone (voice input PRD §3): press to
// record, press again (or the auto-stop cap) to transcribe; the transcript
// inserts at the caret with a trailing space and is never auto-sent. A
// failure is one plain sentence. The state drives the button (a stop button
// while recording, a spinner while transcribing) and the textarea stays
// editable throughout.

export function useVoiceInput({
  setText,
  taRef,
}: {
  // setText inserts the transcript (it receives the whole next value).
  readonly setText: (next: string) => void;
  readonly taRef: RefObject<HTMLTextAreaElement | null>;
}) {
  const [state, setState] = useState<VoiceState>("idle");
  const recorderRef = useRef(openVoiceRecorder());

  useEffect(
    () => () => {
      // Leaving the composer mid-recording: release the microphone.
      recorderRef.current.stopNow();
    },
    [],
  );

  const insert = useCallback(
    (text: string) => {
      const ta = taRef.current;
      const t = text.trim();
      if (!t) return;
      if (!ta) {
        setText(t + " ");
        return;
      }
      const start = ta.selectionStart ?? ta.value.length;
      const end = ta.selectionEnd ?? start;
      const before = ta.value.slice(0, start);
      const after = ta.value.slice(end);
      const join = before === "" || before.endsWith(" ") ? "" : " ";
      const tail = after === "" || after.startsWith(" ") ? "" : " ";
      setText(`${before}${join}${t}${tail}${after}`);
      requestAnimationFrame(() => {
        ta.focus();
        const at = (before + join + t + tail).length;
        ta.setSelectionRange(at, at);
      });
    },
    [setText, taRef],
  );

  const toggle = useCallback(async () => {
    const rec = recorderRef.current;
    try {
      if (state === "idle") {
        setState("recording");
        try {
          await rec.start();
        } catch (e) {
          setState("idle");
          throw e;
        }
        return;
      }
      if (state === "recording") {
        setState("encoding");
        const wav = await rec.stop();
        setState("transcribing");
        const { text } = await transcribeAudio(wav);
        setState("idle");
        insert(text);
      }
    } catch {
      setState("idle");
      throw new Error("Voice input failed: check the microphone and try again");
    }
  }, [insert, state]);

  return { state, toggle };
}
