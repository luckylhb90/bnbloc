import { useCallback, useState } from "react";
import { runJSONAssist, useAssistTask } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import type { AssistJSONReply, AssistRequest } from "@/lib/wire.gen";
import type { AssistRunState } from "@/views/AssistButton";

/** A channel assist (lib/channelAssists): whether it can run here, its
 *  state, and its last failure said where it ran. */
export interface ChannelAssist {
  // null: no model or no such task for the member (the control is then a
  // note that the AI is unavailable, and every field is filled by hand).
  readonly available: boolean;
  readonly state: AssistRunState;
  readonly error: string;
  readonly run: (body: AssistRequest) => Promise<AssistJSONReply["fields"] | null>;
}

// useChannelAssist runs one of the channels' JSON assists by name.
export function useChannelAssist(name: string, what: string): ChannelAssist {
  const task = useAssistTask(name);
  const [state, setState] = useState<AssistRunState>("idle");
  const [error, setError] = useState("");
  const run = useCallback(
    async (body: AssistRequest) => {
      if (!task) return null;
      setState("waiting");
      setError("");
      try {
        return (await runJSONAssist(task.name, body)).fields;
      } catch (e) {
        setError(errorText(e, `Could not ${what}`));
        return null;
      } finally {
        setState("idle");
      }
    },
    [task, what],
  );
  return { available: !!task, state, error, run };
}
