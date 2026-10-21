import { createContext, useContext, useState } from "react";
import {
  ArrowClockwiseIcon,
  ChatCircleIcon,
  DesktopIcon,
  GlobeSimpleIcon,
  LifebuoyIcon,
  MegaphoneIcon,
  PaperclipIcon,
  PuzzlePieceIcon,
  SignpostIcon,
  SparkleIcon,
  XIcon,
} from "@phosphor-icons/react";
import type { MemberComponent } from "@/lib/api";
import { fixLabel, isAgentReply, type DeadEndAction, type DeadEndFix } from "@/lib/deadEnd";
import { errorText } from "@/lib/errors";
import { validPrivateHost } from "@/lib/sandboxStatus";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { Button } from "@/ui/Button";
import { Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { ActorLabel } from "@/views/room/ActorLabel";
import type { FeedItem } from "@/views/room/types";
import type { DeadEndRoom } from "@/views/room/useDeadEnd";
import { ComponentSettingsDialog } from "@/views/settings/ComponentSettingsDialog";

// The "never a dead end" card under an agent's reply and the reply's "Didn't
// work? Find a way" (docs/prd/2026-10-20-never-a-dead-end.md §2.3). They
// reach the memoized Message rows through context (as messageExtras does),
// so a card shows without re-rendering the feed. The card is the member's
// alone: local state, never posted to the room.

const DeadEndContext = createContext<DeadEndRoom | null>(null);
export const DeadEndProvider = DeadEndContext.Provider;

const ICONS: Readonly<Record<DeadEndAction, typeof PuzzlePieceIcon>> = {
  install: PuzzlePieceIcon,
  "ask-agent": ChatCircleIcon,
  attach: PaperclipIcon,
  "own-agent": DesktopIcon,
  rephrase: SparkleIcon,
  report: MegaphoneIcon,
  "allow-host": GlobeSimpleIcon,
};

/** How often the card checks that an allowed host opened. */
const HOST_POLL_MS = 2000;

/** FindAWayButton is an agent reply's "Didn't work? Find a way". */
export function FindAWayButton({ item }: { readonly item: FeedItem }) {
  const room = useContext(DeadEndContext);
  if (!room || !isAgentReply(item) || item.status === "pending") return null;
  return (
    <button
      type="button"
      onClick={() => room.find(item)}
      title="Didn't work? Find a way"
      aria-label="Didn't work? Find a way"
      className="tactile flex items-center rounded-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <LifebuoyIcon size={12} />
    </button>
  );
}

/** DeadEndCard is the card under a reply, when it has one. */
export function DeadEndCard({ item }: { readonly item: FeedItem }) {
  const room = useContext(DeadEndContext);
  const card = room && item.seq != null ? room.cards[item.seq] : undefined;
  if (!room || !card || item.seq == null) return null;
  const seq = item.seq;
  return (
    <section
      data-dead-end
      aria-label="Find a way"
      className="mt-2 flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-3 py-2 text-sm"
    >
      <div className="flex items-start gap-2">
        <SignpostIcon size={16} className="mt-0.5 shrink-0 text-[var(--color-accent)]" />
        <p className="min-w-0 flex-1 break-words">
          {card.status === "finding" ? "Finding a way…" : card.proposal?.summary ?? "Didn't work? These may help."}
        </p>
        <button
          type="button"
          onClick={() => room.dismiss(seq)}
          title="Dismiss"
          aria-label="Dismiss"
          className="tactile shrink-0 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
        >
          <XIcon size={14} />
        </button>
      </div>
      {card.status === "ready" && (
        <>
          {card.note && <p className="text-xs text-[var(--color-fg-muted)]">{card.note}</p>}
          <ul className="flex flex-col gap-1.5">
            {card.fixes.map((fix) => (
              <li key={`${fix.action}:${fix.target}`}>
                <FixRow room={room} reply={item} fix={fix} />
              </li>
            ))}
          </ul>
          <p className="text-[0.68rem] text-[var(--color-fg-subtle)]">Only you see this. Nothing happens until you choose.</p>
        </>
      )}
    </section>
  );
}

// FixRow is one fix: its button and why, and the step a fix needs before
// it acts (an install's confirmation, a report's text to review, a host's
// confirmation, typed when the proposal named none).
function FixRow({ room, reply, fix }: { readonly room: DeadEndRoom; readonly reply: FeedItem; readonly fix: DeadEndFix }) {
  const toast = useToast();
  const [step, setStep] = useState<"idle" | "confirm" | "busy">("idle");
  const [note, setNote] = useState(fix.target);
  // An allowed host: waiting for it to open, then open (ask again).
  const [host, setHost] = useState<{ readonly name: string; readonly open: boolean } | null>(null);
  const [setup, setSetup] = useState<MemberComponent | null>(null);
  // What an install or a report did, shown on the fix that did it.
  const [result, setResult] = useState("");
  const Icon = ICONS[fix.action];
  const component = fix.action === "install" ? room.component(fix.target) : undefined;

  const run = async () => {
    switch (fix.action) {
      case "install":
      case "report":
      case "allow-host":
        setStep("confirm");
        return;
      case "ask-agent":
        room.askAgent(reply, fix.target);
        return;
      case "attach":
        room.attach();
        return;
      case "own-agent":
        room.ownAgent(reply);
        return;
      case "rephrase":
        setStep("busy");
        await room.rephrase(reply);
        setStep("idle");
    }
  };
  const confirm = async () => {
    setStep("busy");
    try {
      if (fix.action === "install") {
        const needsSetup = await room.install(fix.target);
        setSetup(needsSetup);
        setResult(needsSetup ? "Installed: set it up, then ask again" : "Installed: ask again");
      } else if (fix.action === "allow-host") {
        const name = note.trim().toLowerCase();
        await room.allowHost(name);
        setHost({ name, open: false });
        setResult(`Opening ${name} to your agents…`);
      } else {
        const queued = await room.report(reply, note.trim());
        setResult(queued ? "Sent to your admins" : "Not sent: no notification reaches your admins, or they turned room reports off");
      }
      setStep("idle");
    } catch (e) {
      toast.error(errorText(e, fix.action === "install" ? `Could not install ${fix.target}` : fix.action === "allow-host" ? "Could not allow the host" : "Could not send the report"));
      setStep("confirm");
    }
  };

  // Once allowed, the host opens when the member's own VM and their bridge
  // take it (after any run of theirs there ends): then the request can go
  // again.
  useVisiblePoll(
    () => {
      if (!host) return;
      void room
        .sandbox()
        .then((s) => {
          const p = s?.privateHosts;
          if (p && p.state === "ready" && p.applied.includes(host.name)) {
            setHost({ ...host, open: true });
            setResult(`${host.name} is open to your agents`);
          } else if (p?.state === "error") {
            setResult(p.message);
          } else if (p?.state === "waiting") {
            setResult(`Opening ${host.name} when your agent's current run ends…`);
          }
        })
        .catch(() => {});
    },
    HOST_POLL_MS,
    !!host && !host.open,
  );

  const hostName = note.trim().toLowerCase();
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" disabled={step === "busy" || !!result} onClick={() => void run()}>
          <Icon size={14} />
          {fixLabel(fix)}
          {fix.action === "ask-agent" && <ActorLabel actor={fix.target} />}
        </Button>
        {result ? (
          <span role="status" className="min-w-0 text-xs font-medium text-[var(--color-fg)]">
            {result}
          </span>
        ) : (
          fix.why && <span className="min-w-0 text-xs text-[var(--color-fg-muted)]">{fix.why}</span>
        )}
      </div>
      {step !== "idle" && fix.action === "install" && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="min-w-0 flex-1">
            Add <strong>{fix.target}</strong> to your components{component?.description ? `: ${component.description}` : ""}
          </span>
          <Button size="sm" variant="primary" disabled={step === "busy"} onClick={() => void confirm()}>
            Install
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setStep("idle")}>
            Cancel
          </Button>
        </div>
      )}
      {step !== "idle" && fix.action === "allow-host" && (
        <div className="flex flex-col gap-1.5 text-xs">
          {!fix.target && (
            <Input
              className="font-mono"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              aria-label="The private host"
              placeholder="npm.internal.example"
              spellCheck={false}
              autoCapitalize="none"
            />
          )}
          <span className="min-w-0">
            Let your own agents&apos; sandboxed runs reach <strong className="font-mono">{hostName || "this host"}</strong> on any port, and anything else at its
            address. Runs of your agents by other members never reach it. You can remove it in your sandbox&apos;s settings.
          </span>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="primary" disabled={step === "busy" || !validPrivateHost(hostName)} onClick={() => void confirm()}>
              Allow
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setStep("idle")}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      {host?.open && (
        <div>
          <Button size="sm" variant="outline" onClick={() => room.askAgain(reply)}>
            <ArrowClockwiseIcon size={14} />
            Ask again
          </Button>
        </div>
      )}
      {step !== "idle" && fix.action === "report" && (
        <div className="flex flex-col gap-1.5">
          <Textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} aria-label="The note to your admins" placeholder="What you asked for and what is missing" />
          <div className="flex items-center gap-2">
            <Button size="sm" variant="primary" disabled={step === "busy" || !note.trim()} onClick={() => void confirm()}>
              Send to admins
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setStep("idle")}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      <ComponentSettingsDialog target={setup} onClose={() => setSetup(null)} />
    </div>
  );
}
