import { useCallback, useEffect, useReducer, useState } from "react";
import { artifactLinks } from "@/lib/team";
import { onboardingEvents } from "@/lib/onboardingEvents";
import {
  parseWorkbench,
  serializeWorkbench,
  workbenchReducer,
  workbenchStorageKey,
  type DeliverablesFilter,
  type WorkbenchAction,
  type WorkbenchState,
} from "@/lib/workbenchTabs";
import { readStored, writeStored } from "@/lib/storageKeys";

// useWorkbench owns a room's preview workbench: its tabs (persisted per room
// in localStorage, so a member coming back finds the same set) and whether
// the workbench is open. Opening anything opens the workbench; closing the
// last tab closes it. A copied deep link (#/room/<id>?artifact=…) arrives
// through the artifactLinks handoff and opens its tab.

export interface Workbench {
  readonly state: WorkbenchState;
  readonly open: boolean;
  readonly dispatch: (action: WorkbenchAction) => void;
  readonly openArtifact: (artifactId: string, version?: number) => void;
  readonly openDeliverables: (filter?: DeliverablesFilter) => void;
  readonly closeWorkbench: () => void;
}

function storedWorkbench(roomId: string): WorkbenchState {
  return parseWorkbench(readStored(workbenchStorageKey(roomId)) || null);
}

export function useWorkbench(roomId: string): Workbench {
  const [state, dispatch] = useReducer(workbenchReducer, roomId, storedWorkbench);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    writeStored(workbenchStorageKey(roomId), serializeWorkbench(state)); // full or disabled storage: tabs just do not persist
  }, [roomId, state]);

  useEffect(() => {
    if (open && state.tabs.length === 0) setOpen(false);
  }, [open, state.tabs.length]);

  const openArtifact = useCallback(
    (artifactId: string, version?: number) => {
      dispatch({ type: "open", artifactId, version });
      setOpen(true);
      onboardingEvents.emit({ kind: "artifact_opened", roomId });
    },
    [roomId],
  );
  const openDeliverables = useCallback((filter?: DeliverablesFilter) => {
    dispatch({ type: "openDeliverables", filter });
    setOpen(true);
  }, []);
  const closeWorkbench = useCallback(() => setOpen(false), []);

  // Deep links: taken on mount (the link opened this room) and whenever one
  // is queued while the room is open.
  useEffect(() => {
    const take = () => {
      const link = artifactLinks.take(roomId);
      if (link) openArtifact(link.artifactId, link.version);
    };
    take();
    return artifactLinks.subscribe((target) => {
      if (target === roomId) take();
    });
  }, [roomId, openArtifact]);

  return { state, open, dispatch, openArtifact, openDeliverables, closeWorkbench };
}
