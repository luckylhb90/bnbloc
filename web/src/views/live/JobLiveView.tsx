import { useCallback, useEffect, useRef, useState } from "react";
import { AppWindowIcon, ArrowSquareOutIcon, CheckCircleIcon, HandGrabbingIcon, HandPalmIcon, XIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { frameKey, keyInput, modifiers, pointAt, type JobLive } from "@/lib/jobLive";
import { jobLiveApi } from "@/lib/jobLiveApi";
import type { LiveDialog, LiveInput } from "@/lib/wire.gen";
import { inDesktopShell } from "@/lib/config";
import { Button } from "@/ui/Button";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { useToast } from "@/ui/Toast";

// A job's live view (docs/prd/2026-10-17-browser-component.md §2.8): the
// page the agent works on, as the tool last showed it. Small in the job's
// progress strip; large in a workbench tab or its own window, where the
// owner (whose machine and sign-ins it is) can take control and drive the
// page with the pointer and keyboard, then hand it back to the agent.

/** useLiveFrame fetches a job's newest picture whenever a new one exists
 *  (live.seq moves). watch marks the caller as watching the large view: the
 *  tool sends frames faster then. One fetch at a time; a frame that
 *  arrives meanwhile is fetched right after (only the newest). onFacts
 *  receives the facts each answer carries (a view opened between events). */
export function useLiveFrame(
  roomId: string,
  live: Pick<JobLive, "jobId" | "run" | "seq"> | undefined,
  watch: boolean,
  onFacts?: (f: JobLive) => void,
): string | null {
  const [url, setUrl] = useState<string | null>(null);
  const key = live ? frameKey(live) : "";
  const jobId = live?.jobId ?? "";
  const factsRef = useRef(onFacts);
  factsRef.current = onFacts;
  const busy = useRef(false);
  const wanted = useRef("");
  const shown = useRef("");
  const pull = useCallback(() => {
    if (!jobId || busy.current || wanted.current === shown.current) return;
    busy.current = true;
    const target = wanted.current;
    jobLiveApi
      .frame(roomId, jobId, watch)
      .then(({ image, facts }) => {
        shown.current = target;
        if (facts) factsRef.current?.(facts);
        setUrl((old) => {
          if (old) URL.revokeObjectURL(old);
          return URL.createObjectURL(image);
        });
      })
      .catch(() => {
        shown.current = target; // gone (the turn ended); the next event retries
      })
      .finally(() => {
        busy.current = false;
        pull();
      });
  }, [roomId, jobId, watch]);
  useEffect(() => {
    wanted.current = key;
    pull();
  }, [key, pull]);
  // While watched (and visible), keep telling the tool so (the watch lapses
  // in seconds), even when the page stands still.
  useVisiblePoll(
    () =>
      void jobLiveApi
        .frame(roomId, jobId, true)
        .then(({ facts }) => facts && factsRef.current?.(facts))
        .catch(() => {}),
    3000,
    watch && !!jobId,
  );
  useEffect(() => () => setUrl((old) => (old && URL.revokeObjectURL(old), null)), []);
  return url;
}

/** JobLiveThumb is the small view in a job's progress strip: clicking it
 *  opens the large one. */
export function JobLiveThumb({ roomId, live, onOpen }: { roomId: string; live: JobLive; onOpen?: () => void }) {
  const url = useLiveFrame(roomId, live, false);
  if (!url) return null;
  return (
    <button
      type="button"
      onClick={onOpen}
      disabled={!onOpen}
      className="group relative mt-1 block w-full max-w-[22rem] overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] text-left"
      title={onOpen ? "Watch the page live" : live.title}
      aria-label={`Live view${live.title ? `: ${live.title}` : ""}`}
      data-job-live-thumb
    >
      <img src={url} alt="" className="block max-h-40 w-full object-cover object-top" draggable={false} />
      <span className="absolute inset-x-0 bottom-0 flex items-center gap-1 truncate bg-black/55 px-1.5 py-0.5 text-[11px] text-white">
        <span className="size-1.5 shrink-0 rounded-full bg-red-500" aria-hidden />
        <span className="truncate">{live.controller ? `${live.controller} is driving` : live.title || live.url || "Live"}</span>
      </span>
    </button>
  );
}

/** LiveWaitingNote says what a view waits for (a sign-in only its owner can
 *  do, browser component PRD §3.7) and, for the owner, lets the agent
 *  continue (once done) or closes the view. Others read who it waits on. */
export function LiveWaitingNote({
  roomId,
  live,
  subject,
  onContinue,
  onOpen,
}: {
  roomId: string;
  live: JobLive;
  subject: string | null;
  onContinue?: (live: JobLive) => void;
  onOpen?: () => void;
}) {
  const toast = useToast();
  if (!live.waiting) return null;
  const mine = !!subject && live.owner === subject;
  const close = () =>
    void jobLiveApi.input(roomId, live.jobId, [{ type: "close" }]).catch((e: unknown) => toast.error(e instanceof ApiError ? e.message : "Could not close the view"));
  // At the computer the browser runs on, its real window is the surest
  // place for a sign-in (every method works there).
  const focus = () =>
    void jobLiveApi.input(roomId, live.jobId, [{ type: "focus" }]).catch((e: unknown) => toast.error(e instanceof ApiError ? e.message : "Could not bring the browser window forward"));
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs" data-live-waiting-note>
      <span className={cn("size-2 shrink-0 rounded-full", live.ready ? "bg-[var(--color-accent)]" : "bg-[var(--color-warning)]")} aria-hidden />
      <span className="min-w-0 flex-1 text-[var(--color-fg-muted)]">
        {mine ? (live.ready ? "Looks done. Let the agent continue when you are ready." : live.waiting) : `Waiting for ${live.owner}: ${live.waiting}`}
      </span>
      {mine && inDesktopShell() && (
        <Button size="sm" variant="outline" onClick={focus} title="Bring the browser window forward on the computer it runs on: every sign-in method works there">
          <AppWindowIcon size={14} /> Open on this computer
        </Button>
      )}
      {mine && onOpen && (
        <Button size="sm" variant="outline" onClick={onOpen} title="Use the page here, in the room">
          Open
        </Button>
      )}
      {mine && onContinue && (
        <Button size="sm" variant={live.ready ? "primary" : "outline"} onClick={() => onContinue(live)} title="Tell the agent you are done, so it continues">
          <CheckCircleIcon size={14} /> Continue
        </Button>
      )}
      {mine && (
        <Button size="icon" variant="ghost" onClick={close} title="Close the view (the agent's run stays paused)" aria-label="Close the view">
          <XIcon size={14} />
        </Button>
      )}
    </div>
  );
}

/** LiveDialogCard is the page's open dialog (browser UI the picture cannot
 *  show; the page waits on it): its message, and for the controller the
 *  answer. Others read who may answer. */
function LiveDialogCard({ dialog, canAnswer, onAnswer }: { dialog: LiveDialog; canAnswer: boolean; onAnswer: (accept: boolean, text?: string) => void }) {
  const [text, setText] = useState(dialog.default ?? "");
  const question = dialog.kind === "beforeunload" ? "Leave this page? Changes you made may not be saved." : dialog.message;
  return (
    <div className="absolute inset-0 z-20 grid place-items-center bg-black/40 p-4" data-live-dialog>
      <div role="alertdialog" aria-label="The page asks" className="w-full max-w-sm rounded-[var(--radius-lg)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] p-4 text-sm shadow-[var(--shadow-diffuse)]">
        <p className="mb-1 text-xs text-[var(--color-fg-subtle)]">The page asks</p>
        <p className="whitespace-pre-wrap break-words">{question || "(no message)"}</p>
        {canAnswer ? (
          <form
            className="mt-3 flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              onAnswer(true, dialog.kind === "prompt" ? text : undefined);
            }}
          >
            {dialog.kind === "prompt" && (
              <input autoFocus value={text} onChange={(e) => setText(e.target.value)} className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5" aria-label="Your answer" />
            )}
            <div className="flex justify-end gap-2">
              {dialog.kind !== "alert" && (
                <Button type="button" size="sm" variant="outline" onClick={() => onAnswer(false)}>
                  {dialog.kind === "beforeunload" ? "Stay" : "Cancel"}
                </Button>
              )}
              <Button type="submit" size="sm" variant="primary">
                {dialog.kind === "beforeunload" ? "Leave" : "OK"}
              </Button>
            </div>
          </form>
        ) : (
          <p className="mt-3 text-xs text-[var(--color-fg-muted)]">Whoever drives the page answers it.</p>
        )}
      </div>
    </div>
  );
}

// How often a held control is renewed (it lapses server-side after
// LiveControlLapseMs without one).
const RENEW_MS = 5_000;
// Pointer moves of a drag are sent at most this often.
const MOVE_MS = 50;

// isPasteKey: Cmd+V / Ctrl+V, left to the browser's paste event.
function isPasteKey(e: { readonly key: string; readonly metaKey: boolean; readonly ctrlKey: boolean }): boolean {
  return (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "v";
}

// isImeKey: a key an input method takes (Chinese, Japanese, Korean...): it
// composes in the view's own text field and arrives as text once chosen.
// WebKit reports the key that commits a composition as 229 after its
// compositionend, so the code is checked too.
function isImeKey(e: React.KeyboardEvent): boolean {
  return e.nativeEvent.isComposing || e.key === "Process" || e.keyCode === 229;
}

/** JobLiveView is the large view: the picture, who drives, take over /
 *  hand back for the owner, and "Open in the browser window" on their
 *  machine. */
export function JobLiveView({
  roomId,
  live,
  subject,
  onPopOut,
  bootstrap,
  onContinue,
}: {
  roomId: string;
  live: JobLive | undefined;
  subject: string | null;
  onPopOut?: () => void;
  // Lets the agent continue a view waiting for its owner (§3.7).
  onContinue?: (live: JobLive) => void;
  // A window opened without the room's events so far (a pop-out) asks for
  // the job's frame by id; its facts arrive with the image.
  bootstrap?: { jobId: string; onFacts: (f: JobLive) => void };
}) {
  const toast = useToast();
  const url = useLiveFrame(roomId, live ?? (bootstrap ? { jobId: bootstrap.jobId, run: "", seq: 0 } : undefined), true, bootstrap?.onFacts);
  const imgRef = useRef<HTMLImageElement>(null);
  const queue = useRef<LiveInput[]>([]);
  const flushing = useRef(false);
  const lastMove = useRef(0);
  // The keys land in a hidden text field over the picture: an input
  // method composes there (its candidates show where the person clicked)
  // and what it commits goes to the page as text.
  const keysRef = useRef<HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState({ left: 0, top: 0 });
  const jobId = live?.jobId ?? "";
  const mine = !!subject && live?.owner === subject;
  const driving = mine && live?.controller === subject;
  const other = live?.controller && live.controller !== subject ? live.controller : "";

  const flush = useCallback(() => {
    if (flushing.current || queue.current.length === 0 || !jobId) return;
    flushing.current = true;
    const events = queue.current.splice(0, queue.current.length);
    jobLiveApi
      .input(roomId, jobId, events)
      .catch((e: unknown) => toast.error(e instanceof ApiError ? e.message : "The input did not reach the browser"))
      .finally(() => {
        flushing.current = false;
        flush();
      });
  }, [roomId, jobId, toast]);
  const send = useCallback(
    (e: LiveInput) => {
      queue.current.push(e);
      flush();
    },
    [flush],
  );

  // Hold control while driving and looking: renew it (a hidden view lets it
  // lapse, so the agent goes on), and hand it back on leaving.
  useVisiblePoll(() => void jobLiveApi.control(roomId, jobId, true).catch(() => {}), RENEW_MS, driving);
  useEffect(() => {
    if (!driving) return;
    const release = () => void jobLiveApi.control(roomId, jobId, false).catch(() => {});
    window.addEventListener("pagehide", release);
    return () => {
      window.removeEventListener("pagehide", release);
      release();
    };
  }, [driving, roomId, jobId]);

  // A view waiting for its owner (a sign-in) is theirs to use: control is
  // taken as they open it, once (handing it back stays theirs).
  const waitingForMe = mine && !!live?.waiting && !live.controller;
  const tookFor = useRef("");
  useEffect(() => {
    if (!waitingForMe || tookFor.current === jobId) return;
    tookFor.current = jobId;
    void jobLiveApi.control(roomId, jobId, true).catch(() => {});
  }, [waitingForMe, roomId, jobId]);

  // The room's job_live_frame event (realtime publishes one on every
  // control change) moves live.controller; nothing is kept here.
  const setControl = (take: boolean) => {
    if (!jobId) return;
    jobLiveApi.control(roomId, jobId, take).catch((e: unknown) => toast.error(e instanceof ApiError ? e.message : "Could not change control"));
  };
  const point = (e: React.PointerEvent | React.WheelEvent) => {
    const img = imgRef.current;
    if (!img || !live) return null;
    return pointAt(img.getBoundingClientRect(), { width: live.width, height: live.height }, e.clientX, e.clientY);
  };
  const button = (b: number) => (b === 2 ? "right" : b === 1 ? "middle" : "left");
  // What an input method put in the field goes to the page, once.
  const sendTyped = () => {
    const el = keysRef.current;
    if (!el || !el.value) return;
    send({ type: "text", text: el.value });
    el.value = "";
  };

  if (!live && !(bootstrap && url)) {
    return (
      <div className="grid h-full place-items-center p-6 text-center text-sm text-[var(--color-fg-muted)]" data-job-live>
        The run is not showing a page right now. It appears here while the agent works.
      </div>
    );
  }
  if (!live) return null;
  return (
    <div className="flex h-full min-h-0 flex-col" data-job-live>
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-xs">
        <span className="size-2 shrink-0 rounded-full bg-red-500" aria-hidden />
        <span className="min-w-0 flex-1 truncate text-[var(--color-fg-muted)]" title={live.url}>
          {live.title || live.url || "Live page"}
        </span>
        {other && <span className="text-[var(--color-fg-subtle)]">{other} is driving</span>}
        {mine && !driving && !other && (
          <Button size="sm" variant="outline" onClick={() => setControl(true)} title="Pause the agent and use the page yourself">
            <HandGrabbingIcon size={14} /> Take over
          </Button>
        )}
        {driving && !live.waiting && (
          <Button size="sm" variant="primary" onClick={() => setControl(false)} title="Let the agent continue from here">
            <HandPalmIcon size={14} /> Hand back
          </Button>
        )}
        {mine && (
          <Button size="sm" variant="ghost" onClick={() => send({ type: "focus" })} title="Bring the browser window to the front on your computer (for a security key, a passkey or a system dialog)">
            <AppWindowIcon size={14} /> Open in the browser window
          </Button>
        )}
        {onPopOut && (
          <Button size="icon" variant="ghost" onClick={onPopOut} title="Open in its own window" aria-label="Open in its own window">
            <ArrowSquareOutIcon size={16} />
          </Button>
        )}
      </div>
      <div className={cn("relative min-h-0 flex-1 overflow-hidden bg-[var(--color-surface)]", driving && "ring-2 ring-inset ring-[var(--color-accent)]")}>
        {driving && (
          <textarea
            ref={keysRef}
            aria-label="Type on the page"
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
            spellCheck={false}
            rows={1}
            data-live-keys
            style={caret}
            className="pointer-events-none absolute z-10 h-6 w-px resize-none overflow-hidden border-0 bg-transparent p-0 text-[16px] text-transparent caret-transparent opacity-0 outline-none"
            onKeyDown={(e) => {
              // An input method's keys compose here; paste stays the
              // browser's own (its paste event carries this computer's
              // clipboard). Every other key goes to the page as a key.
              if (isImeKey(e) || isPasteKey(e)) return;
              e.preventDefault();
              send(keyInput("down", e));
            }}
            onKeyUp={(e) => {
              if (isImeKey(e) || isPasteKey(e)) return;
              e.preventDefault();
              send(keyInput("up", e));
            }}
            onCompositionEnd={sendTyped}
            onInput={(e) => {
              // Text outside a composition here came from an input method
              // too (its punctuation): the keys above typed none.
              if (!(e.nativeEvent as InputEvent).isComposing) sendTyped();
            }}
            onPaste={(e) => {
              const text = e.clipboardData.getData("text/plain");
              e.preventDefault();
              if (text) send({ type: "text", text });
            }}
          />
        )}
        {live.dialog && <LiveDialogCard key={live.dialog.kind + live.dialog.message} dialog={live.dialog} canAnswer={driving} onAnswer={(accept, text) => send({ type: "dialog", action: accept ? "accept" : "dismiss", ...(text !== undefined ? { text } : {}) })} />}
        {url ? (
          <img
            ref={imgRef}
            src={url}
            alt={live.title || "The page the agent works on"}
            draggable={false}
            className={cn("absolute inset-0 h-full w-full select-none object-contain", driving ? "cursor-default" : "cursor-not-allowed")}
            onContextMenu={(e) => driving && e.preventDefault()}
            // The press must not take the focus from the keys' field.
            onMouseDown={(e) => driving && e.preventDefault()}
            onPointerDown={(e) => {
              if (!driving) return;
              const p = point(e);
              if (!p) return;
              const box = e.currentTarget.getBoundingClientRect();
              setCaret({ left: e.clientX - box.left, top: e.clientY - box.top });
              keysRef.current?.focus({ preventScroll: true });
              e.currentTarget.setPointerCapture(e.pointerId);
              send({ type: "mouse", action: "down", ...p, button: button(e.button), clicks: e.detail || 1, modifiers: modifiers(e) });
            }}
            onPointerUp={(e) => {
              if (!driving) return;
              const p = point(e);
              if (!p) return;
              send({ type: "mouse", action: "up", ...p, button: button(e.button), clicks: e.detail || 1, modifiers: modifiers(e) });
            }}
            onPointerMove={(e) => {
              // Only a drag moves the page's pointer: hover moves would queue
              // ahead of the keys and slow typing to a crawl.
              if (!driving || e.buttons === 0 || Date.now() - lastMove.current < MOVE_MS) return;
              const p = point(e);
              if (!p) return;
              lastMove.current = Date.now();
              send({ type: "mouse", action: "move", ...p, modifiers: modifiers(e) });
            }}
            onWheel={(e) => {
              if (!driving) return;
              const p = point(e);
              if (!p) return;
              send({ type: "wheel", ...p, deltaX: e.deltaX, deltaY: e.deltaY, modifiers: modifiers(e) });
            }}
          />
        ) : (
          <div className="grid h-full place-items-center text-sm text-[var(--color-fg-subtle)]">Loading the page…</div>
        )}
      </div>
      {live.waiting ? (
        <div className="shrink-0 border-t border-[var(--color-border)] px-3 py-1.5">
          <LiveWaitingNote roomId={roomId} live={live} subject={subject} onContinue={onContinue} />
        </div>
      ) : (
        mine &&
        !driving && (
          <p className="shrink-0 border-t border-[var(--color-border)] px-3 py-1.5 text-[11px] text-[var(--color-fg-subtle)]">
            Take over to use the page yourself: the agent waits until you hand it back.
          </p>
        )
      )}
    </div>
  );
}
