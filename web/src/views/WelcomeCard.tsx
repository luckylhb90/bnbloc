import { useEffect, useState } from "react";
import { CloudIcon, DownloadSimpleIcon, SparkleIcon, XIcon } from "@phosphor-icons/react";
import { api, ApiError, type Room } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { inDesktopShell } from "@/lib/config";
import { openMacInstallGuide } from "@/lib/macInstall";
import {
  canDismissWelcome,
  defaultPlatformChoice,
  dismissWelcome,
  firstRoomId,
  firstRoomName,
  showWelcome,
  welcomeDismissed,
} from "@/lib/onboarding";
import type { PlatformChoice } from "@/lib/platformAgents";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { browserStorage } from "@/lib/platformRouting";
import { composerAgentPicks } from "@/lib/team";
import { routeHash } from "@/lib/useHashRoute";
import { usePlatformAgents } from "@/lib/usePlatformAgents";
import { autoStartTour } from "@/lib/useOnboarding";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";

// Room ids are global: a taken id is retried with a fresh suffix.
const CREATE_ATTEMPTS = 3;

// WelcomeCard is the first-run card on the home views (PRD 2026-10-10 v2 §1):
// "Start a room with <agent>" creates a private room, adds the default
// platform agent, opens the room and selects the agent — so a browser-only
// member reaches an agent reply with one click plus typing. The secondary
// path is the desktop app (own agents). `rooms` is the member's room list
// when the view already has it; otherwise the card reads it.
export function WelcomeCard({ rooms, onOpen }: { readonly rooms?: readonly Room[] | null; readonly onOpen: (id: string) => void }) {
  const { subject, reauth } = useAuth();
  const toast = useToast();
  const { agents: offered } = usePlatformAgents();
  const [ownRooms, setOwnRooms] = useState<readonly Room[] | null>(null);
  const [dismissed, setDismissed] = useState(() => welcomeDismissed(browserStorage(), subject));
  const [starting, setStarting] = useState(false);

  const known = rooms !== undefined;
  useEffect(() => {
    if (known || !subject) return;
    let cancelled = false;
    api
      .myRooms(subject)
      .then((r) => !cancelled && setOwnRooms(r.rooms ?? []))
      .catch(() => !cancelled && setOwnRooms(null));
    return () => {
      cancelled = true;
    };
  }, [known, subject]);
  useEffect(() => setDismissed(welcomeDismissed(browserStorage(), subject)), [subject]);

  const list = known ? rooms : ownRooms;
  const roomCount = list ? list.length : null;
  const choice = defaultPlatformChoice(offered, subject);
  const visible = showWelcome({ roomCount, dismissed });
  // A brand-new member is guided from this first screen: the tour starts on
  // the start button (only when there is an agent to start a room with).
  const canStart = visible && !!choice;
  useEffect(() => {
    if (canStart) autoStartTour(subject, roomCount);
  }, [canStart, subject, roomCount]);
  if (!visible) return null;
  const desktop = inDesktopShell();

  const start = async (c: PlatformChoice) => {
    setStarting(true);
    const ref = { core: c.agent.core ?? "", provider: c.agent.provider };
    try {
      const name = firstRoomName((list ?? []).map((r) => r.name || r.roomId));
      const roomId = await createFirstRoom(name);
      // Refresh the session's room scope so the new room is reachable.
      await reauth().catch(() => {});
      await platformAgentsApi.add(roomId, ref);
      composerAgentPicks.queue(roomId, ref);
      onOpen(roomId);
    } catch (e) {
      toast.error(e instanceof ApiError ? `Could not start the room: ${e.message}` : "Could not start the room");
    } finally {
      setStarting(false);
    }
  };

  const openDesktop = () => {
    window.location.hash = routeHash({ kind: "view", view: "releases" });
    openMacInstallGuide();
  };

  return (
    <section
      aria-labelledby="welcome-title"
      className="animate-in relative mb-6 overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] p-5"
    >
      {canDismissWelcome(roomCount) && (
        <button
          type="button"
          onClick={() => {
            dismissWelcome(browserStorage(), subject);
            setDismissed(true);
          }}
          className="tactile absolute right-2 top-2 grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
          aria-label="Dismiss the welcome card"
          title="Dismiss"
        >
          <XIcon size={16} />
        </button>
      )}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
        <div className="grid size-11 shrink-0 place-items-center rounded-full bg-[var(--color-surface)] text-[var(--color-accent)]">
          <SparkleIcon size={22} weight="fill" />
        </div>
        <div className="min-w-0 flex-1 pr-8">
          <h2 id="welcome-title" className="text-base font-semibold tracking-tight text-[var(--color-fg)]">
            Try bnbloc with an AI agent
          </h2>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            {choice
              ? `Start a private room with ${choice.agent.provider}, a platform agent — nothing to install. Ask it to build a page, write a plan or explain an idea.`
              : desktop
                ? "No platform agent is available right now — an admin can enable platform agents for your organization. You can also connect your own agent core from the Agents page."
                : "No platform agent is available right now — an admin can enable platform agents for your organization. Meanwhile, install the desktop app to use your own agents."}
          </p>
        </div>
      </div>
      <div className="mt-4 flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
        {choice && (
          <Button
            variant="primary"
            onClick={() => void start(choice)}
            disabled={starting}
            className="w-full sm:w-auto"
            data-tour="welcome-start"
          >
            <CloudIcon size={16} weight="fill" />
            {starting ? "Starting…" : `Start a room with ${choice.agent.provider}`}
          </Button>
        )}
        {!desktop && (
          <Button variant={choice ? "ghost" : "primary"} onClick={openDesktop} className="w-full sm:w-auto">
            <DownloadSimpleIcon size={16} />
            Install the desktop app to use your own agents
          </Button>
        )}
      </div>
    </section>
  );
}

// createFirstRoom creates the private first room under a fresh global id and
// returns that id.
async function createFirstRoom(name: string): Promise<string> {
  for (let attempt = 1; ; attempt++) {
    const roomId = firstRoomId();
    try {
      await api.createRoom({ roomId, name, visibility: "private" });
      return roomId;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 409) || attempt >= CREATE_ATTEMPTS) throw e;
    }
  }
}
