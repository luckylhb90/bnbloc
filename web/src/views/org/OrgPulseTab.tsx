import { useEffect, useRef, useState, type RefObject } from "react";
import { PulseIcon } from "@phosphor-icons/react";
import { Card, Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { EmptyState } from "@/ui/Page";
import { cn } from "@/lib/cn";
import { compactNumber, exactTime, formatUsd } from "@/lib/format";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import {
  appWritesToday,
  deliverablesToday,
  fmt,
  freshness,
  pct,
  summaryText,
  tokenRatePerMin,
  type OrgPulseSnapshot,
} from "@/lib/orgPulse";
import { useOrgPulse } from "@/views/org/useOrgPulse";
import { OrgPulseScene, type SceneCanvases, type SceneData } from "@/views/org/orgPulseScene";

// OrgPulseTab is the organization board inside the org console. It reads the same
// snapshot and stream the server publishes, through the session the console
// already has. A figure the server did not send is shown as absent, never zero.
export function OrgPulseTab() {
  const board = useOrgPulse();
  const [now, setNow] = useState(() => Date.now());
  const root = useRef<HTMLDivElement>(null);
  const [scene] = useState(() => new OrgPulseScene());
  const rate = board.snap ? tokenRatePerMin(board.samples, now) : null;
  // Member locations: true/false once the organization's setting is read; null while it loads,
  // when no pulses are drawn at all.
  const policy = useTenantPolicy();
  const geoOn = policy ? policy.pulseGeography : null;

  // The clock ticks while the page is visible; no data is requested by it.
  useVisiblePoll(() => setNow(Date.now()), 1000);

  // The pictures take the latest figures; a rise in deliverables lands bricks.
  useEffect(() => {
    if (board.snap) scene.update(sceneData(board.snap, rate, geoOn), Date.now());
    // rate is recomputed each second; the snapshot and the setting are the changes that matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [board.snap, scene, geoOn]);

  // One animation loop for the tab, reading the canvases inside its own root.
  useEffect(() => {
    let frame = 0;
    const loop = () => {
      const canvases = findCanvases(root);
      if (canvases) scene.draw(canvases, Date.now());
      frame = requestAnimationFrame(loop);
    };
    frame = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(frame);
  }, [scene]);

  if (board.forbidden) {
    return (
      <EmptyState
        icon={<PulseIcon size={28} />}
        title="The board is for org admins"
        hint="Your account does not have the organization board permission. An org admin or super admin can see it."
      />
    );
  }
  if (!board.snap) {
    if (board.error) return <ErrorBox>{board.error}</ErrorBox>;
    return <BoardSkeleton />;
  }

  const snap = board.snap;
  const fresh = freshness(snap, now);
  return (
    <div ref={root} className="flex flex-col gap-4">
      <BoardStatus connected={board.connected} fresh={fresh} seenAt={board.seenAt} />
      {board.error && <ErrorBox>{board.error}</ErrorBox>}
      <KpiRow snap={snap} />
      <div className="grid gap-4 md:grid-cols-5">
        <div className="md:col-span-3">
          <GlobeCard geoOn={geoOn} withheld={snap.geo.countriesWithheld} />
        </div>
        <div className="md:col-span-2">
          <PumpCard snap={snap} rate={rate} />
        </div>
      </div>
      <div className="grid gap-4 md:grid-cols-5">
        <div className="md:col-span-3">
          <FieldCard snap={snap} peak={board.peakWorking} />
        </div>
        <div className="md:col-span-2">
          <SummaryCard snap={snap} feed={board.feed} />
        </div>
      </div>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Figures from {exactTime(snap.settlingSinceMs)} may still change while they settle. People counts under 5
        are withheld.
      </p>
    </div>
  );
}

// findCanvases returns the board's six canvases, or null while any is missing.
function findCanvases(root: RefObject<HTMLDivElement | null>): SceneCanvases | null {
  const el = root.current;
  if (!el) return null;
  const pick = (name: string) => el.querySelector<HTMLCanvasElement>(`canvas[data-pulse="${name}"]`);
  const c = { globe: pick("globe"), swarm: pick("swarm"), arc: pick("arc"), stack: pick("stack"), pump: pick("pump"), field: pick("field") };
  if (!c.globe || !c.swarm || !c.arc || !c.stack || !c.pump || !c.field) return null;
  return c as SceneCanvases;
}

function sceneData(snap: OrgPulseSnapshot, rate: number | null, geoOn: boolean | null): SceneData {
  return {
    peopleOnline: snap.live.peopleSuppressed ? null : snap.live.peopleOnline,
    coverage: snap.today.coverage,
    deliverables: deliverablesToday(snap),
    tokensPerMin: rate,
    tokensToday: snap.today.tokensIn + snap.today.tokensOut,
    agentsWorking: snap.live.agentsWorking,
    places: geoOn === true ? snap.geo.countries.map((c) => ({ lat: c.lat, lon: c.lon, people: c.people })) : [],
    illustrative: geoOn === false,
  };
}

function BoardSkeleton() {
  return (
    <div className="flex flex-col gap-4">
      <Skeleton className="h-44" />
      <Skeleton className="h-80" />
      <Skeleton className="h-64" />
    </div>
  );
}

function BoardStatus({
  connected,
  fresh,
  seenAt,
}: {
  readonly connected: boolean;
  readonly fresh: ReturnType<typeof freshness>;
  readonly seenAt: number;
}) {
  let tone = "bg-[var(--color-ok)]";
  let text: string;
  if (!connected) {
    tone = "bg-[var(--color-danger)]";
    text = `disconnected · values from ${exactTime(seenAt)}`;
  } else if (fresh.state === "none") {
    text = "live · no rollup written yet";
  } else if (fresh.state === "delayed") {
    tone = "bg-[var(--color-warn)]";
    text = `delayed · data ${fmt(fresh.behindSec)} s behind`;
  } else {
    text = `live · data ${fmt(fresh.behindSec)} s behind`;
  }
  return (
    <div className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
      <span className={cn("h-2 w-2 rounded-full", tone)} aria-hidden="true" />
      {text}
    </div>
  );
}

function KpiRow({ snap }: { readonly snap: OrgPulseSnapshot }) {
  const t = snap.today;
  return (
    <div className="grid gap-4 md:grid-cols-3">
      <Card className="flex flex-col gap-2 p-4">
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">People online now</div>
        <canvas data-pulse="swarm" className="h-28 w-full" aria-hidden="true" />
        <div className="text-2xl font-semibold tabular-nums">
          {snap.live.peopleSuppressed || snap.live.peopleOnline === null ? "under 5" : fmt(snap.live.peopleOnline)}
        </div>
        <div className="text-xs text-[var(--color-fg-muted)]">
          {snap.live.peopleSuppressed ? "withheld: fewer than 5 people (k = 5)" : "connected now (presence)"}
        </div>
      </Card>
      <Card className="flex flex-col gap-2 p-4">
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Usage coverage today</div>
        <canvas data-pulse="arc" className="h-28 w-full" aria-hidden="true" />
        <div className="text-2xl font-semibold tabular-nums">{t.coverage === null ? "—" : pct(t.coverage)}</div>
        <div className="text-xs text-[var(--color-fg-muted)]">
          {t.replies > 0
            ? `${fmt(t.repliesReported)} of ${fmt(t.replies)} agent replies report usage`
            : "no agent replies yet today"}
        </div>
      </Card>
      <Card className="flex flex-col gap-2 p-4">
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Deliverables today</div>
        <canvas data-pulse="stack" className="h-28 w-full" aria-hidden="true" />
        <div className="text-2xl font-semibold tabular-nums">{fmt(deliverablesToday(snap))}</div>
        <div className="text-xs text-[var(--color-fg-muted)]">
          new versions by members · app writes today: {fmt(appWritesToday(snap))}
        </div>
      </Card>
    </div>
  );
}

function GlobeCard({ geoOn, withheld }: { readonly geoOn: boolean | null; readonly withheld: number }) {
  let note: string;
  if (geoOn === true) {
    note = `Members by country, from their time zones. Countries with fewer than 5 people are not shown${
      withheld ? ` (${fmt(withheld)} withheld)` : ""}.`;
  } else if (geoOn === false) {
    note = "Illustrative only. Member locations are off for this organization, so these pulses are random places, not activity.";
  } else {
    note = "Loading the organization's location setting.";
  }
  return (
    <Card className="flex flex-col gap-2 p-4">
      <div className="flex items-baseline justify-between gap-2">
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Where it is day now</div>
        {geoOn === false && (
          <span className="rounded-full border border-[var(--color-border)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">
            illustrative
          </span>
        )}
      </div>
      <canvas data-pulse="globe" className="h-80 w-full" role="img" aria-label="Globe showing day and night" />
      <div className="text-xs text-[var(--color-fg-subtle)]">{note}</div>
    </Card>
  );
}

function PumpCard({ snap, rate }: { readonly snap: OrgPulseSnapshot; readonly rate: number | null }) {
  const t = snap.today;
  const tokens = t.tokensIn + t.tokensOut;
  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Token pump · reported usage</div>
      <div className="flex gap-4">
        <canvas data-pulse="pump" className="h-48 w-24 shrink-0" aria-hidden="true" />
        <div className="flex min-w-0 flex-col gap-1">
          <div className="text-3xl font-semibold tabular-nums">{fmt(tokens)}</div>
          <div className="text-xs text-[var(--color-fg-muted)]">
            tokens today · {rate === null ? "not enough history for a rate" : `${compactNumber(rate)} per min`}
          </div>
          <div className="text-xs text-[var(--color-fg-muted)]">
            ≈ {formatUsd(t.costUsd)} reported · coverage {t.coverage === null ? "—" : pct(t.coverage)}
          </div>
          <Lanes snap={snap} />
        </div>
      </div>
    </Card>
  );
}

function Lanes({ snap }: { readonly snap: OrgPulseSnapshot }) {
  const entries = Object.entries(snap.today.repliesByCore);
  if (entries.length === 0) {
    return <div className="mt-2 text-xs text-[var(--color-fg-subtle)]">No agent replies today.</div>;
  }
  return (
    <div className="mt-2 flex flex-col gap-1.5 text-xs text-[var(--color-fg-muted)]">
      {entries.map(([core, lane]) => (
        <div key={core} className="flex items-center gap-2">
          <span className="w-24 truncate">{core}</span>
          <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--color-border)]">
            <span
              className="block h-full rounded-full bg-[var(--color-accent)]"
              style={{ width: lane.replies ? `${(lane.reported / lane.replies) * 100}%` : "0%" }}
            />
          </span>
          <span className="whitespace-nowrap">
            {lane.reported === 0 ? "not reported" : `${fmt(lane.reported)} of ${fmt(lane.replies)}`}
          </span>
        </div>
      ))}
    </div>
  );
}

function FieldCard({ snap, peak }: { readonly snap: OrgPulseSnapshot; readonly peak: number }) {
  const idle = snap.live.agentsWorking === 0 && snap.live.agentsWaiting === 0 && snap.today.replies === 0;
  return (
    <Card className="flex flex-col gap-2 p-4">
      <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Agents at work</div>
      <canvas data-pulse="field" className="h-48 w-full" aria-hidden="true" />
      <div className="flex flex-wrap gap-5 text-sm text-[var(--color-fg-muted)]">
        <span>
          <span className="text-xl font-semibold text-[var(--color-fg)]">{fmt(snap.live.agentsWorking)}</span> working
        </span>
        <span>
          <span className="text-xl font-semibold text-[var(--color-fg)]">{fmt(snap.live.agentsWaiting)}</span> waiting
          for a bridge
          {snap.live.oldestWaitingSec !== null && (
            <span className="text-xs"> · oldest {fmt(snap.live.oldestWaitingSec)} s</span>
          )}
        </span>
        <span>peak this session {fmt(peak)}</span>
      </div>
      {idle && (
        <div className="text-xs text-[var(--color-fg-subtle)]">
          No agent has run today. Agent work needs a bridge connected to a local agent.
        </div>
      )}
    </Card>
  );
}

function SummaryCard({ snap, feed }: { readonly snap: OrgPulseSnapshot; readonly feed: { t: number; text: string }[] }) {
  return (
    <Card className="flex flex-col gap-3 p-4">
      <div>
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Today so far</div>
        <p className="mt-1 text-sm text-[var(--color-fg)]">{summaryText(snap)}</p>
        <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">Built from the numbers. No model writes it.</p>
      </div>
      <div>
        <div className="text-xs uppercase tracking-wide text-[var(--color-fg-muted)]">Changes since this page opened</div>
        {feed.length === 0 ? (
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">No changes yet.</p>
        ) : (
          <ul className="mt-1 flex max-h-72 flex-col gap-1.5 overflow-auto text-xs text-[var(--color-fg-muted)]">
            {feed.map((item, i) => (
              <li key={`${item.t}-${i}`} className="flex gap-2">
                <time className="tabular-nums text-[var(--color-fg-subtle)]">{exactTime(item.t)}</time>
                <span>{item.text}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}
