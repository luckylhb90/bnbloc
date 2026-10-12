import { useEffect, useState } from "react";
import { CloudIcon, DesktopIcon, DownloadSimpleIcon, SparkleIcon, XIcon } from "@phosphor-icons/react";
import { api, ApiError, type RoomSummary } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { can } from "@/lib/permissions";
import { inDesktopShell } from "@/lib/config";
import { useDesktopPresence } from "@/lib/desktopPresence";
import { openMacInstallGuide } from "@/lib/macInstall";
import {
  WELCOME_DESKTOP_HINT,
  WELCOME_NO_AGENT,
  canDismissWelcome,
  dismissWelcome,
  firstRoomId,
  firstRoomName,
  showWelcome,
  welcomeDesktopAction,
  welcomeDismissed,
  welcomeStart,
} from "@/lib/onboarding";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { browserStorage } from "@/lib/platformRouting";
import { composerAgentPicks } from "@/lib/team";
import { routeHash } from "@/lib/useHashRoute";
import { usePlatformAgents } from "@/lib/usePlatformAgents";
import { useQuery } from "@/lib/useQuery";
import { autoStartTour } from "@/lib/useOnboarding";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// Room ids are global: a taken id is retried with a fresh suffix.
const CREATE_ATTEMPTS = 3;

// WelcomeCard is the first-run card on the home views (PRD 2026-10-10 v2 §1):
// "Start a room with <agent>" creates a private room, adds the default
// platform agent, opens the room and selects the agent — so a browser-only
// member reaches an agent reply with one click plus typing. The secondary
// path is the desktop app (own agents). `rooms` is the member's room list
// when the view already has it; otherwise the card reads it.
export function WelcomeCard({ rooms, onOpen }: { readonly rooms?: readonly RoomSummary[] | null; readonly onOpen: (id: string) => void }) {
  const { subject, reauth, role } = useAuth();
  // Starting a room creates one and drives an agent: a read-only (viewer)
  // organization role does neither.
  const mayStart = can(role, "create_room") && can(role, "drive_agent");
  const toast = useToast();
  const { agents: offered } = usePlatformAgents();
  const presence = useDesktopPresence();
  const [ownRooms, setOwnRooms] = useState<readonly RoomSummary[] | null>(null);
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
  // The member's own agents: a member who hosts every platform agent (or has
  // agents of their own) starts with one of those (welcomeStart).
  const own = useQuery(subject ? `my-agents:${subject}` : null, api.myAgents, { staleMs: 30_000 });
  const startWith = welcomeStart(offered, own.data?.agents ?? [], subject);
  const visible = showWelcome({ roomCount, dismissed });
  // A brand-new member is guided from this first screen: the tour starts on
  // the start button (only when there is an agent to start a room with).
  const canStart = visible && mayStart && startWith.kind !== "none";
  useEffect(() => {
    if (canStart) autoStartTour(subject, roomCount);
  }, [canStart, subject, roomCount]);
  if (!visible) return null;
  const desktop = welcomeDesktopAction({
    inShell: inDesktopShell(),
    applicable: presence.applicable,
    connected: presence.connected,
    installed: presence.installed,
    openFailed: presence.openFailed,
  });

  const start = async () => {
    if (startWith.kind === "none") return;
    setStarting(true);
    const ref =
      startWith.kind === "platform"
        ? { core: startWith.choice.agent.core ?? "", provider: startWith.choice.agent.provider, own: false }
        : { core: startWith.agent.core, provider: startWith.agent.provider, own: true };
    try {
      const name = firstRoomName((list ?? []).map((r) => r.name || r.roomId));
      const roomId = await createFirstRoom(name);
      // Refresh the session's room scope so the new room is reachable.
      await reauth().catch(() => {});
      // A platform agent joins the room through a grant; the member's own
      // agent needs none (its bridge serves the owner's rooms).
      if (!ref.own) await platformAgentsApi.add(roomId, { core: ref.core, provider: ref.provider });
      composerAgentPicks.queue(roomId, ref);
      onOpen(roomId);
    } catch (e) {
      toast.error(errorText(e, "Could not start the room"));
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
            {startWith.kind === "platform"
              ? `Start a private room with ${startWith.choice.agent.provider}, a platform agent — nothing to install. Ask it to build a page, write a plan or explain an idea.`
              : startWith.kind === "own"
                ? `Start a private room with your ${startWith.agent.provider} agent. Ask it to build a page, write a plan or explain an idea.`
                : [WELCOME_NO_AGENT[startWith.reason], WELCOME_DESKTOP_HINT[desktop]].filter(Boolean).join(" ")}
          </p>
        </div>
      </div>
      <div className="mt-4 flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
        {mayStart && startWith.kind !== "none" && (
          <Button
            variant="primary"
            onClick={() => void start()}
            disabled={starting}
            className="w-full sm:w-auto"
            data-tour="welcome-start"
          >
            <CloudIcon size={16} weight="fill" />
            {starting
              ? "Starting…"
              : startWith.kind === "platform"
                ? `Start a room with ${startWith.choice.agent.provider}`
                : `Start a room with your ${startWith.agent.provider}`}
          </Button>
        )}
        {desktop === "open" && (
          <Button variant={startWith.kind !== "none" ? "ghost" : "primary"} onClick={presence.open} disabled={presence.checking} className="w-full sm:w-auto">
            <DesktopIcon size={16} />
            {presence.checking ? "Opening…" : "Open the desktop app"}
          </Button>
        )}
        {desktop === "install" && (
          <Button variant={startWith.kind !== "none" ? "ghost" : "primary"} onClick={openDesktop} className="w-full sm:w-auto">
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
