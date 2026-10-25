// The organization board's pictures. Each figure is drawn from the snapshot the
// server sent; nothing here invents a number. A place with no data is simply
// not drawn (the globe has no pulses: locations are not recorded).
import { LAND_BITS } from "./orgPulseLand.ts";

export type SceneData = {
  // People online, null when the server withheld the count (fewer than 5).
  peopleOnline: number | null;
  coverage: number | null; // reported / replies today; null with no replies
  deliverables: number;
  tokensPerMin: number | null;
  tokensToday: number;
  agentsWorking: number;
  // Pulses on the globe. Real ones are countries with their people online.
  places: { lat: number; lon: number; people: number }[];
  // true when member locations are off: the pulses are random land points,
  // drawn to show the globe's motion, and labelled as illustrative.
  illustrative: boolean;
};

export type SceneCanvases = {
  globe: HTMLCanvasElement;
  swarm: HTMLCanvasElement;
  arc: HTMLCanvasElement;
  stack: HTMLCanvasElement;
  pump: HTMLCanvasElement;
  field: HTMLCanvasElement;
};

const TILE_MAX = 200; // tiles drawn one for one; beyond that the count is text only
const PEOPLE_MAX_POINTS = 300;
const toRad = Math.PI / 180;

type Pt = { fromA: number; hr: number; ha: number; born: number; dieAt: number; fading: boolean; sp: number };
type Landed = { t: number; c: number };
type Bubble = { x: number; y: number; r: number; s: number };

let landCells: { lat: number; lon: number }[] | null = null;
function land(): { lat: number; lon: number }[] {
  if (landCells) return landCells;
  const cols = 180, rows = 90;
  const bin = atob(LAND_BITS);
  const out: { lat: number; lon: number }[] = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const i = r * cols + c;
      if (bin.charCodeAt(i >> 3) & (1 << (7 - (i & 7)))) out.push({ lat: 89 - r * 2, lon: -180 + c * 2 + 1 });
    }
  }
  landCells = out;
  return out;
}

let glowSprite: HTMLCanvasElement | null = null;
function glow(): HTMLCanvasElement {
  if (glowSprite) return glowSprite;
  const c = document.createElement("canvas");
  c.width = c.height = 32;
  const g = c.getContext("2d");
  if (g) {
    const grd = g.createRadialGradient(16, 16, 0, 16, 16, 16);
    grd.addColorStop(0, "rgba(190,220,255,1)");
    grd.addColorStop(0.3, "rgba(57,135,229,0.55)");
    grd.addColorStop(1, "rgba(57,135,229,0)");
    g.fillStyle = grd;
    g.fillRect(0, 0, 32, 32);
  }
  glowSprite = c;
  return c;
}

// fit sizes a canvas to its box and returns a context in CSS pixels.
function fit(c: HTMLCanvasElement): { ctx: CanvasRenderingContext2D; w: number; h: number } | null {
  const dpr = Math.min(2, window.devicePixelRatio || 1);
  const w = c.clientWidth, h = c.clientHeight;
  if (c.width !== Math.round(w * dpr) || c.height !== Math.round(h * dpr)) {
    c.width = Math.round(w * dpr);
    c.height = Math.round(h * dpr);
  }
  const ctx = c.getContext("2d");
  if (!ctx) return null;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, w, h };
}

function solarDeclination(d: Date): number {
  const n = Math.floor((d.getTime() - Date.UTC(d.getUTCFullYear(), 0, 0)) / 86400000);
  return 23.44 * Math.sin((2 * Math.PI / 365) * (n - 81));
}

export class OrgPulseScene {
  private data: SceneData | null = null;
  private reduce = false;
  private rot = -40;
  private pts: Pt[] = [];
  private rings: number[] = [];
  private band: number | null = null;
  private arcShown: number | null = null;
  private bricks = 0;
  private landed: Landed[] = [];
  private lastDeliv: number | null = null;
  private pumpLevel = 0.06;
  private illus: { lat: number; lon: number; t0: number }[] = [];
  private nextIllus = 0;
  private bubbles: Bubble[] = Array.from({ length: 26 }, () => ({
    x: Math.random(), y: Math.random(), r: 1.2 + Math.random() * 2, s: 0.2 + Math.random() * 0.4,
  }));

  constructor() {
    this.reduce = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  // update takes the latest figures. A rise in deliverables lands bricks now.
  update(d: SceneData, nowMs: number): void {
    if (this.lastDeliv !== null && d.deliverables > this.lastDeliv) {
      const k = Math.min(d.deliverables - this.lastDeliv, 6);
      for (let i = 0; i < k; i++) this.landed.push({ t: nowMs, c: Math.floor(Math.random() * 5) });
    }
    this.lastDeliv = d.deliverables;
    const raw = d.peopleOnline ?? 0;
    const band = Math.floor(raw / 5);
    if (this.band !== null && band !== this.band) this.rings.push(nowMs);
    this.band = band;
    this.data = d;
  }

  draw(c: SceneCanvases, nowMs: number): void {
    this.drawGlobe(c.globe, nowMs);
    this.drawSwarm(c.swarm, nowMs);
    this.drawArc(c.arc, nowMs);
    this.drawStack(c.stack, nowMs);
    this.drawPump(c.pump, nowMs);
    this.drawField(c.field, nowMs);
  }

  // Globe: land dots and the real day/night terminator for the current UTC time.
  private drawGlobe(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const R = Math.min(w, h) * 0.46, cx = w / 2, cy = h / 2, phi0 = 14 * toRad, lam0 = this.rot * toRad;
    const d = new Date(now);
    const utcH = d.getUTCHours() + d.getUTCMinutes() / 60;
    const sLon = (180 - utcH * 15) * toRad, sLat = solarDeclination(d) * toRad;
    const halo = ctx.createRadialGradient(cx, cy, R * 0.9, cx, cy, R * 1.25);
    halo.addColorStop(0, "rgba(57,135,229,0.20)");
    halo.addColorStop(1, "rgba(57,135,229,0)");
    ctx.fillStyle = halo;
    ctx.beginPath(); ctx.arc(cx, cy, R * 1.25, 0, Math.PI * 2); ctx.fill();
    for (const q of land()) {
      const p = q.lat * toRad, l = q.lon * toRad - lam0;
      const cosc = Math.sin(phi0) * Math.sin(p) + Math.cos(phi0) * Math.cos(p) * Math.cos(l);
      if (cosc <= 0) continue;
      const x = cx + R * Math.cos(p) * Math.sin(l);
      const y = cy - R * (Math.cos(phi0) * Math.sin(p) - Math.sin(phi0) * Math.cos(p) * Math.cos(l));
      const sun = Math.sin(p) * Math.sin(sLat) + Math.cos(p) * Math.cos(sLat) * Math.cos(q.lon * toRad - sLon);
      const day = sun > 0, edge = Math.abs(sun) < 0.12;
      const a = (day ? 0.55 : 0.12) * (0.4 + 0.6 * cosc);
      ctx.fillStyle = edge ? `rgba(120,170,240,${a + 0.12})` : day ? `rgba(200,215,240,${a})` : `rgba(90,110,150,${a})`;
      ctx.beginPath();
      ctx.arc(x, y, Math.max(1.1, R * 0.011) * (0.6 + 0.4 * cosc), 0, Math.PI * 2);
      ctx.fill();
    }
    // Pulses: a point is drawn only when it faces the viewer.
    const proj = (lat: number, lon: number) => {
      const p = lat * toRad, l = lon * toRad - lam0;
      const cosc = Math.sin(phi0) * Math.sin(p) + Math.cos(phi0) * Math.cos(p) * Math.cos(l);
      return {
        vis: cosc > 0,
        x: cx + R * Math.cos(p) * Math.sin(l),
        y: cy - R * (Math.cos(phi0) * Math.sin(p) - Math.sin(phi0) * Math.cos(p) * Math.cos(l)),
      };
    };
    if (this.data?.illustrative) {
      const cells = land();
      if (!this.reduce && now >= this.nextIllus && cells.length) {
        this.nextIllus = now + 2400;
        const q = cells[(Math.random() * cells.length) | 0];
        this.illus.push({ lat: q.lat, lon: q.lon, t0: now });
      }
      this.illus = this.illus.filter((p) => now - p.t0 < 2600);
      for (const p of this.illus) this.drawPulse(ctx, proj, p.lat, p.lon, (now - p.t0) / 2600, 0.5);
    }
    for (const pl of this.data?.places ?? []) {
      const strength = Math.min(1, 0.4 + Math.log10(1 + pl.people) / 2);
      this.drawPulse(ctx, proj, pl.lat, pl.lon, this.reduce ? 0 : (now / 1600) % 1, strength);
    }
    if (!this.reduce) this.rot += 0.035;
    if (this.rot > 180) this.rot -= 360;
  }

  // drawPulse draws a dot and a ring at a place, the ring at phase k in [0, 1).
  private drawPulse(
    ctx: CanvasRenderingContext2D,
    proj: (lat: number, lon: number) => { vis: boolean; x: number; y: number },
    lat: number,
    lon: number,
    k: number,
    strength: number,
  ): void {
    const p = proj(lat, lon);
    if (!p.vis) return;
    ctx.strokeStyle = `rgba(57,135,229,${(1 - k) * 0.6 * strength})`;
    ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.arc(p.x, p.y, 3 + k * 16, 0, Math.PI * 2); ctx.stroke();
    ctx.fillStyle = `rgba(120,180,255,${0.5 + 0.4 * strength})`;
    ctx.beginPath(); ctx.arc(p.x, p.y, 2.6, 0, Math.PI * 2); ctx.fill();
  }

  // People online: one soft point per person, only when the server gave a count.
  private drawSwarm(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const want = this.data && this.data.peopleOnline !== null ? Math.min(PEOPLE_MAX_POINTS, this.data.peopleOnline) : 0;
    const alive = this.pts.filter((p) => !p.fading);
    if (alive.length < want) {
      for (let i = alive.length; i < want; i++) {
        this.pts.push({ fromA: Math.random() * 6.28, hr: Math.sqrt(Math.random()) * 0.82, ha: Math.random() * 6.28, born: now, dieAt: 0, fading: false, sp: 0.2 + Math.random() * 0.3 });
      }
    } else if (alive.length > want) {
      for (let i = 0; i < alive.length - want; i++) {
        const p = alive[(Math.random() * alive.length) | 0];
        if (!p.fading) { p.fading = true; p.dieAt = now; }
      }
    }
    this.pts = this.pts.filter((p) => !p.fading || now - p.dieAt < 900);
    this.rings = this.rings.filter((t) => now - t < 1400);
    const cx = w / 2, cy = h / 2, R = Math.min(w, h) * 0.46;
    ctx.strokeStyle = "rgba(255,255,255,0.07)"; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.arc(cx, cy, R, 0, Math.PI * 2); ctx.stroke();
    for (const t of this.rings) {
      const k = (now - t) / 1400;
      ctx.strokeStyle = `rgba(57,135,229,${(1 - k) * 0.7})`; ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(cx, cy, 6 + k * R * 1.1, 0, Math.PI * 2); ctx.stroke();
    }
    for (const p of this.pts) {
      const ease = 1 - Math.pow(1 - Math.min(1, (now - p.born) / 900), 3);
      const tt = this.reduce ? 0 : now / 1000;
      const hx = Math.cos(p.ha + tt * p.sp) * p.hr * R, hy = Math.sin(p.ha + tt * p.sp * 0.8) * p.hr * R;
      const ex = cx + Math.cos(p.fromA) * R * 1.15, ey = cy + Math.sin(p.fromA) * R * 1.15;
      const x = ex + (cx + hx - ex) * ease, y = ey + (cy + hy - ey) * ease;
      let alpha = 0.35 + 0.5 * ease;
      if (p.fading) alpha *= Math.max(0, 1 - (now - p.dieAt) / 900);
      if (alpha <= 0.01) continue;
      ctx.globalAlpha = alpha;
      ctx.drawImage(glow(), x - 7, y - 7, 14, 14);
    }
    ctx.globalAlpha = 1;
  }

  // Usage coverage: the arc is reported / replies today. No replies, an empty track.
  private drawArc(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const target = this.data?.coverage ?? 0;
    if (this.arcShown === null) this.arcShown = target;
    this.arcShown += (target - this.arcShown) * 0.05;
    const cx = w / 2, cy = h / 2 + 4, R = Math.min(w, h) * 0.42, start = -Math.PI / 2, full = Math.PI * 2;
    for (let i = 0; i < 40; i++) {
      const a = start + (i / 40) * full, r0 = R + 6, r1 = R + (i % 4 === 0 ? 12 : 9);
      ctx.strokeStyle = "rgba(255,255,255,0.12)"; ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(cx + Math.cos(a) * r0, cy + Math.sin(a) * r0);
      ctx.lineTo(cx + Math.cos(a) * r1, cy + Math.sin(a) * r1);
      ctx.stroke();
    }
    ctx.lineWidth = 9; ctx.lineCap = "round";
    ctx.strokeStyle = "rgba(255,255,255,0.07)";
    ctx.beginPath(); ctx.arc(cx, cy, R, start, start + full); ctx.stroke();
    if (this.arcShown > 0.001) {
      const end = start + full * this.arcShown;
      ctx.shadowColor = "rgba(57,135,229,0.8)"; ctx.shadowBlur = 14;
      ctx.strokeStyle = "rgba(57,135,229,0.95)";
      ctx.beginPath(); ctx.arc(cx, cy, R, start, end); ctx.stroke();
      ctx.shadowBlur = 0;
      const sweep = this.reduce ? 0 : (now / 1800) % 1;
      const tipA = end - sweep * Math.min(full * 0.12, end - start);
      const bx = cx + Math.cos(tipA) * R, by = cy + Math.sin(tipA) * R;
      const g = ctx.createRadialGradient(bx, by, 0, bx, by, 12);
      g.addColorStop(0, "rgba(230,240,255,0.95)");
      g.addColorStop(1, "rgba(57,135,229,0)");
      ctx.fillStyle = g;
      ctx.beginPath(); ctx.arc(bx, by, 12, 0, Math.PI * 2); ctx.fill();
    }
    ctx.lineCap = "butt";
  }

  // Deliverables: the stack's height follows the count on a log scale; each new one lands a brick.
  private drawStack(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const dl = this.data?.deliverables ?? 0;
    const target = Math.min(14, Math.max(0, Math.log2(dl + 1) * 2.2));
    this.bricks += (target - this.bricks) * 0.08;
    const cols = 5, bw = 26, bh = 11, gx = 4, gy = 3;
    const baseX = w / 2 - (cols * (bw + gx)) / 2, baseY = h - 12;
    const rows = Math.round(this.bricks);
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const x = baseX + c * (bw + gx) + (r % 2) * 6 - 3, y = baseY - (r + 1) * (bh + gy);
        ctx.fillStyle = "rgba(57,135,229,0.22)";
        ctx.beginPath(); ctx.roundRect(x, y, bw, bh, 2); ctx.fill();
        ctx.strokeStyle = "rgba(57,135,229,0.45)"; ctx.lineWidth = 1; ctx.stroke();
      }
    }
    for (const b of this.landed) {
      const k = Math.min(1, (now - b.t) / 650), ease = 1 - Math.pow(1 - k, 3);
      const x = baseX + b.c * (bw + gx), y = baseY - (rows + 1) * (bh + gy) - 40 * (1 - ease);
      ctx.globalAlpha = Math.min(1, k * 2);
      ctx.fillStyle = "rgba(57,135,229,0.85)";
      ctx.beginPath(); ctx.roundRect(x, y, bw, bh, 2); ctx.fill();
      if (k < 1) {
        ctx.fillStyle = "rgba(255,255,255,0.45)";
        ctx.fillRect(x + k * (bw + 20) - 10, y, 3, bh);
      }
      ctx.globalAlpha = 1;
    }
    this.landed = this.landed.filter((b) => now - b.t < 650);
  }

  // Token pump: the column rises with the reported tokens per minute, on a log scale.
  private drawPump(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const rate = this.data?.tokensPerMin ?? 0;
    const target = Math.min(1, Math.max(0.06, Math.log10(1 + rate) / 3.2));
    this.pumpLevel += (target - this.pumpLevel) * 0.06;
    const x0 = 14, x1 = w - 14, yTop = 10, yBot = h - 10;
    ctx.strokeStyle = "rgba(255,255,255,0.14)"; ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.roundRect(x0, yTop, x1 - x0, yBot - yTop, 14); ctx.stroke();
    const level = yBot - (yBot - yTop - 6) * this.pumpLevel;
    const g = ctx.createLinearGradient(0, level, 0, yBot);
    g.addColorStop(0, "rgba(57,135,229,0.85)");
    g.addColorStop(1, "rgba(57,135,229,0.25)");
    ctx.fillStyle = g;
    ctx.beginPath();
    ctx.moveTo(x0 + 2, yBot);
    for (let x = x0 + 2; x <= x1 - 2; x += 4) {
      const wave = this.reduce ? 0 : Math.sin(x * 0.12 + now / 380) * 3 + Math.sin(x * 0.05 - now / 610) * 2;
      ctx.lineTo(x, level + wave);
    }
    ctx.lineTo(x1 - 2, yBot); ctx.closePath(); ctx.fill();
    for (const b of this.bubbles) {
      const speed = this.reduce ? 0 : (0.05 + 0.5 * this.pumpLevel) * b.s;
      b.y = (b.y - speed * 0.016) % 1;
      if (b.y < 0) b.y += 1;
      const py = level + (yBot - level) * (1 - b.y);
      if (py < level + 4) continue;
      ctx.fillStyle = "rgba(200,225,255,0.35)";
      ctx.beginPath(); ctx.arc(x0 + b.x * (x1 - x0 - 12) + 6, py, b.r, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Agents at work: one tile per working job, up to 200; the exact count is in the text.
  private drawField(cv: HTMLCanvasElement, now: number): void {
    const f = fit(cv);
    if (!f) return;
    const { ctx, w, h } = f;
    ctx.clearRect(0, 0, w, h);
    const n = Math.min(this.data?.agentsWorking ?? 0, TILE_MAX);
    const cols = Math.max(12, Math.ceil(Math.sqrt(Math.max(n, 1) * (w / h))));
    const rows = Math.max(3, Math.ceil(Math.max(n, 1) / cols));
    const gap = 4, tw = Math.min((w - gap * (cols - 1)) / cols, (h - gap * (rows - 1)) / rows, 26);
    const ox = (w - (tw * cols + gap * (cols - 1))) / 2, oy = (h - (tw * rows + gap * (rows - 1))) / 2;
    for (let i = 0; i < cols * rows; i++) {
      const c = i % cols, r = Math.floor(i / cols);
      ctx.fillStyle = "rgba(255,255,255,0.035)";
      ctx.beginPath(); ctx.roundRect(ox + c * (tw + gap), oy + r * (tw + gap), tw, tw, 4); ctx.fill();
    }
    for (let i = 0; i < n; i++) {
      const c = i % cols, r = Math.floor(i / cols);
      const x = ox + c * (tw + gap), y = oy + r * (tw + gap);
      const breathe = this.reduce ? 1 : 0.78 + 0.22 * Math.sin(now / 1000 * 2.2 + i);
      ctx.fillStyle = `rgba(57,135,229,${0.55 * breathe})`;
      ctx.beginPath(); ctx.roundRect(x, y, tw, tw, 4); ctx.fill();
      ctx.fillStyle = `rgba(200,225,255,${0.35 * breathe})`;
      ctx.beginPath(); ctx.roundRect(x + tw * 0.2, y + tw * 0.2, tw * 0.6, tw * 0.6, 3); ctx.fill();
    }
  }
}
