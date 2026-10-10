import { useEffect, useRef } from "react";
import { cn } from "@/lib/cn";

// allow: SIZE_OK — this file intentionally carries the required inline world-map RLE data.
const MAP_DOT_STEP = 9;
const MIN_NODE_DISTANCE = 78;
const NODE_IGNITE_SECONDS = 1.25;
const NODE_FADE_SECONDS = 2.7;
const MIN_NODE_HOLD_SECONDS = 5.5;
const MAX_NODE_HOLD_SECONDS = 10.5;
const MIN_NODE_CYCLE_SECONDS = 12;
const MAX_NODE_CYCLE_SECONDS = 24;
const LINK_DRAW_SECONDS = 1.35;
const LINK_HOLD_SECONDS = 1.8;
const LINK_FADE_SECONDS = 2.1;
const MIN_LINK_CYCLE_SECONDS = 7.5;
const MAX_LINK_CYCLE_SECONDS = 18;
const MAP_DRIFT_PIXELS = 3.6;
const MAX_DELTA_SECONDS = 0.05;
const SEED = 0xB10B10C;
const BASE_NODE_AREA = 35_000;
const MIN_NODE_COUNT = 26;
const MAX_NODE_COUNT = 50;
const LAND_MASK_WIDTH = 160;
const LAND_MASK_HEIGHT = 80;
const LAND_LAT_MAX = 84;
const LAND_LAT_MIN = -56;
const MAP_ASPECT = 360 / (LAND_LAT_MAX - LAND_LAT_MIN);

// Natural Earth 110m land polygons sampled into a 160x80 equirectangular bitmask,
// row-run encoded as start-bit:counts. Antarctica is omitted for a lighter footer.
const LAND_MASK_RLE = [
  "0:45,1,1,2,11,9,91", "0:40,11,1,23,85", "0:37,4,1,5,4,21,13,3,1,3,29,4,35", "0:30,1,11,3,3,23,15,2,1,2,69",
  "0:25,2,1,2,1,1,2,3,1,2,9,1,2,19,36,3,12,8,30", "0:25,1,29,16,34,1,12,11,31", "0:24,3,1,2,1,2,1,4,2,4,1,1,10,14,33,2,6,1,1,1,2,21,5,2,16", "0:10,1,16,7,3,2,1,8,9,13,21,2,11,1,5,2,1,38,8,1",
  "0:7,12,1,7,2,3,1,2,1,1,1,2,2,2,2,4,7,12,19,8,11,2,1,2,1,42,1,4", "1:2,5,37,2,1,1,4,4,9,21,12,1,1,1,11,1,47", "0:1,3,1,37,5,3,1,1,4,7,6,5,11,5,1,4,3,62", "0:9,31,1,3,4,3,6,5,8,2,12,5,1,69,1",
  "0:6,33,5,4,2,1,7,3,21,6,1,63,1,5,2", "0:7,5,1,1,1,23,8,3,11,1,21,6,2,2,1,56,3,4,4", "0:10,2,7,20,6,7,31,1,1,2,3,53,8,1,8", "0:9,1,1,1,9,19,6,7,24,2,5,3,2,53,7,3,8",
  "0:7,1,14,21,2,9,24,1,10,51,9,3,8", "0:23,20,2,10,21,1,1,2,3,61,5,2,9", "0:23,21,1,10,23,3,1,62,16", "0:23,1,1,25,1,1,2,1,25,62,1,1,16",
  "0:26,25,3,2,22,64,18", "0:25,26,28,17,1,5,2,37,19", "0:25,25,1,1,27,6,2,6,4,4,2,38,2,1,16", "0:25,24,27,5,4,1,2,4,6,3,2,35,4,2,16",
  "0:25,22,29,5,5,2,1,8,1,4,2,34,4,1,17", "0:25,21,30,4,7,1,1,1,2,10,2,29,3,1,5,1,17", "0:26,20,30,4,6,1,2,1,2,10,2,29,3,1,5,1,17", "0:26,20,34,5,11,37,3,2,2,3,17",
  "0:27,18,32,8,11,37,5,3,19", "0:28,16,32,11,2,1,5,39,4,1,21", "0:28,1,1,14,32,12,1,45,26", "0:29,9,5,1,31,26,2,31,26",
  "0:29,1,1,6,6,1,30,21,1,6,2,30,26", "0:30,1,1,5,36,22,1,6,5,2,1,23,27", "0:33,4,36,23,1,9,4,22,1,1,26", "0:33,4,5,1,1,2,26,24,1,9,5,9,1,9,30",
  "0:33,4,3,1,5,1,26,23,2,8,6,6,3,6,33", "0:34,7,4,1,1,4,22,24,1,7,7,5,5,5,1,1,31", "0:36,5,32,24,2,5,9,4,5,6,5,1,26", "0:39,4,29,26,1,3,11,3,7,5,5,1,26",
  "0:41,2,30,27,13,3,9,4,31", "0:42,1,4,1,25,26,3,1,11,1,11,2,6,2,24", "0:42,1,3,7,21,29,11,1,19,1,25", "0:45,9,20,28,13,1,8,1,9,2,24",
  "0:46,9,20,6,1,20,33,1,24", "0:46,11,19,1,7,17,22,1,1,1,5,2,27", "0:45,12,27,17,22,3,3,3,28", "0:45,13,26,15,25,2,2,5,1,1,25",
  "0:44,14,26,15,26,1,3,3,1,2,3,2,20", "0:45,16,23,14,27,2,2,3,1,1,5,4,17", "0:44,19,22,13,28,1,6,2,5,5,2,1,12", "0:44,20,21,12,30,1,13,6,13",
  "0:45,20,21,11,32,2,10,3,1,1,14", "0:45,19,22,12,37,1,9,2,13", "0:46,17,23,12,62", "0:46,17,23,12,40,3,2,1,16",
  "0:46,17,22,13,3,1,34,1,1,2,3,2,15", "0:48,15,22,13,2,2,33,6,2,2,14,1", "0:49,13,23,11,4,2,32,11,15", "0:49,13,24,9,5,2,31,13,14",
  "0:49,13,24,10,3,2,30,15,14", "0:49,11,26,10,3,2,30,16,13", "0:49,10,28,8,36,17,12", "0:49,9,29,8,36,17,12",
  "0:48,10,29,7,37,17,12", "0:48,10,30,6,37,17,12", "0:48,9,31,5,38,5,3,9,12", "0:48,8,32,2,41,2,7,7,13",
  "0:48,7,87,5,10,1,2", "0:47,8,87,5,13", "0:47,5,105,2,1", "0:47,4,93,2,10,1,3",
  "0:48,3,94,1,10,1,3", "0:47,4,104,1,4", "0:47,3,110", "0:47,4,109",
  "0:46,4,110", "0:47,2,4,1,106", "0:49,1,110", "0:49,2,109",
] as const;

// Node identity hues sit beside --color-accent: teal stays dominant, with muted cyan,
// blue, violet, and a rare amber spark for premium contrast rather than rainbow noise.
const NODE_COLORS = [
  { name: "teal", core: "hsl(168 62% 48%)", glow: "hsl(168 76% 58%)", link: "hsl(168 62% 52%)" },
  { name: "aqua", core: "hsl(188 58% 55%)", glow: "hsl(188 72% 62%)", link: "hsl(188 58% 58%)" },
  { name: "blue", core: "hsl(218 52% 58%)", glow: "hsl(218 66% 66%)", link: "hsl(218 52% 60%)" },
  { name: "violet", core: "hsl(258 46% 62%)", glow: "hsl(258 58% 70%)", link: "hsl(258 48% 64%)" },
  { name: "amber", core: "hsl(38 58% 58%)", glow: "hsl(38 70% 64%)", link: "hsl(38 58% 60%)" },
] as const;
const NODE_COLOR_SEQUENCE = [0, 0, 0, 1, 0, 2, 0, 3, 0, 1, 4, 0, 2, 0, 0, 1] as const;

interface Point { readonly x: number; readonly y: number }
interface Size { readonly width: number; readonly height: number }
interface MapFrame { readonly x: number; readonly y: number; readonly width: number; readonly height: number }
interface GeoPoint { readonly lon: number; readonly lat: number }
interface Candidate extends Point { readonly lat: number; readonly rank: number }
interface NodeColor { readonly name: string; readonly core: string; readonly glow: string; readonly link: string }
interface AgentNode extends Point { readonly cycleOffset: number; readonly cyclePeriod: number; readonly holdSeconds: number; readonly phase: number; readonly color: NodeColor }
interface AgentLink { readonly from: number; readonly to: number; readonly cycleOffset: number; readonly cyclePeriod: number; readonly phase: number }
interface LandDot extends Point { readonly alpha: number; readonly radius: number }
interface Scene extends Size { readonly nodes: readonly AgentNode[]; readonly links: readonly AgentLink[]; readonly landDots: readonly LandDot[] }
interface Palette { readonly bg: string; readonly surface: string; readonly border: string; readonly subtle: string; readonly accent: string }
interface SizedScene { readonly ctx: CanvasRenderingContext2D; readonly scene: Scene }
interface Frame { readonly ctx: CanvasRenderingContext2D; readonly palette: Palette; readonly scene: Scene; readonly time: number; readonly staticFrame: boolean }
interface NodeState { readonly alpha: number; readonly bloom: number; readonly pulse: number }

function decodeLandMask(): readonly (readonly boolean[])[] {
  return LAND_MASK_RLE.map((row) => {
    const separator = row.indexOf(":");
    const startBit = row.slice(0, separator);
    const counts = row.slice(separator + 1).split(",");
    let isLand = startBit === "1";
    const cells: boolean[] = [];
    for (const countText of counts) {
      const count = Number.parseInt(countText, 10);
      for (let index = 0; index < count; index += 1) cells.push(isLand);
      isLand = !isLand;
    }
    return cells.slice(0, LAND_MASK_WIDTH);
  });
}

const LAND_MASK = decodeLandMask();

function mulberry32(seed: number) {
  let state = seed;
  return () => {
    state += 0x6d2b79f5;
    let value = Math.imul(state ^ (state >>> 15), state | 1);
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61);
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
}

function easeOutCubic(value: number) {
  const t = Math.min(Math.max(value, 0), 1);
  return 1 - Math.pow(1 - t, 3);
}

function easeInOutCubic(value: number) {
  const t = Math.min(Math.max(value, 0), 1);
  return t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
}

function wrappedTime(time: number, offset: number, period: number) {
  return (time + offset) % period;
}

function withAlpha(color: string, opacity: number) {
  const value = color.trim();
  if (value.startsWith("hsl(") && !value.includes("/")) return value.replace(")", ` / ${opacity})`);
  if (value.startsWith("rgb(") && !value.includes("/")) return value.replace("rgb(", "rgba(").replace(")", `, ${opacity})`);
  return value;
}

function cssToken(name: string, fallback: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function readPalette(): Palette {
  return {
    bg: cssToken("--color-bg", "hsl(240 6% 7%)"),
    surface: cssToken("--color-surface", "hsl(240 5% 10%)"),
    border: cssToken("--color-border", "hsl(240 5% 20%)"),
    subtle: cssToken("--color-fg-subtle", "hsl(240 4% 46%)"),
    accent: cssToken("--color-accent", "hsl(168 62% 42%)"),
  };
}

function distance(a: Point, b: Point) {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

function nodeCountFor(size: Size) {
  return Math.min(
    MAX_NODE_COUNT,
    Math.max(MIN_NODE_COUNT, Math.round((size.width * size.height) / BASE_NODE_AREA)),
  );
}

function mapFrameFor(size: Size): MapFrame {
  const width = Math.min(size.width * 0.98, size.height * 2.45);
  const height = width / MAP_ASPECT;
  return { x: (size.width - width) / 2, y: size.height * 0.5 - height * 0.5, width, height };
}

function projectGeo(point: GeoPoint, frame: MapFrame): Point {
  return {
    x: frame.x + ((point.lon + 180) / 360) * frame.width,
    y: frame.y + ((LAND_LAT_MAX - point.lat) / (LAND_LAT_MAX - LAND_LAT_MIN)) * frame.height,
  };
}

function geoForCell(column: number, row: number): GeoPoint {
  return {
    lon: -180 + ((column + 0.5) / LAND_MASK_WIDTH) * 360,
    lat: LAND_LAT_MAX - ((row + 0.5) / LAND_MASK_HEIGHT) * (LAND_LAT_MAX - LAND_LAT_MIN),
  };
}

function colorForNode(index: number): NodeColor {
  const colorIndex = NODE_COLOR_SEQUENCE[index % NODE_COLOR_SEQUENCE.length];
  return NODE_COLORS[colorIndex] ?? NODE_COLORS[0];
}

function rangeValue(rng: () => number, min: number, max: number) {
  return min + rng() * (max - min);
}

function landAt(column: number, row: number) {
  return LAND_MASK[row]?.[column] === true;
}

function isCoastCell(column: number, row: number) {
  return !landAt(column - 1, row) || !landAt(column + 1, row) || !landAt(column, row - 1) || !landAt(column, row + 1);
}

function buildLandDots(size: Size, rng: () => number): readonly LandDot[] {
  const frame = mapFrameFor(size);
  const dots: LandDot[] = [];
  for (let row = 0; row < LAND_MASK_HEIGHT; row += 1) {
    for (let column = 0; column < LAND_MASK_WIDTH; column += 1) {
      if (!landAt(column, row)) continue;
      const projected = projectGeo(geoForCell(column, row), frame);
      const latticeX = Math.round(projected.x / MAP_DOT_STEP) * MAP_DOT_STEP;
      const latticeY = Math.round(projected.y / MAP_DOT_STEP) * MAP_DOT_STEP;
      if (latticeX < 12 || latticeX > size.width - 12 || latticeY < 16 || latticeY > size.height - 16) continue;
      const coast = isCoastCell(column, row);
      dots.push({ x: latticeX, y: latticeY, alpha: coast ? 0.14 + rng() * 0.055 : 0.065 + rng() * 0.045, radius: coast ? 1.08 + rng() * 0.24 : 0.88 + rng() * 0.22 });
    }
  }
  return dots;
}

function buildCandidates(size: Size, rng: () => number): readonly Candidate[] {
  const frame = mapFrameFor(size);
  const candidates: Candidate[] = [];
  for (let row = 0; row < LAND_MASK_HEIGHT; row += 1) {
    for (let column = 0; column < LAND_MASK_WIDTH; column += 1) {
      if (!landAt(column, row)) continue;
      const geo = geoForCell(column, row);
      if (geo.lat < -45 || geo.lat > 68) continue;
      const point = projectGeo(geo, frame);
      if (point.x < 44 || point.x > size.width - 44 || point.y < 44 || point.y > size.height - 44) continue;
      const midLatitudeWeight = 1 + Math.max(0, 1 - Math.abs(geo.lat - 24) / 58) * 1.8;
      candidates.push({ ...point, lat: geo.lat, rank: rng() / midLatitudeWeight });
    }
  }
  return candidates.sort((a, b) => a.rank - b.rank);
}

function buildScene(size: Size): Scene {
  const rng = mulberry32(SEED);
  const nodeCount = nodeCountFor(size);
  const candidates = buildCandidates(size, rng);
  const nodes: AgentNode[] = [];
  for (const candidate of candidates) {
    if (nodes.length >= nodeCount) break;
    if (nodes.every((node) => distance(node, candidate) >= MIN_NODE_DISTANCE)) {
      nodes.push({
        x: candidate.x,
        y: candidate.y,
        cycleOffset: rng() * MAX_NODE_CYCLE_SECONDS,
        cyclePeriod: rangeValue(rng, MIN_NODE_CYCLE_SECONDS, MAX_NODE_CYCLE_SECONDS),
        holdSeconds: rangeValue(rng, MIN_NODE_HOLD_SECONDS, MAX_NODE_HOLD_SECONDS),
        phase: rng() * Math.PI * 2,
        color: colorForNode(nodes.length),
      });
    }
  }
  const links: AgentLink[] = [];
  for (let index = 1; index < nodes.length; index += 1) {
    const node = nodes[index];
    if (!node) continue;
    const count = 1 + (index % 3 === 0 ? 0 : 1) + (index % 7 === 0 ? 1 : 0);
    const nearest = nodes
      .slice(0, index)
      .map((other, otherIndex) => ({ otherIndex, length: distance(node, other) }))
      .sort((a, b) => a.length - b.length)
      .slice(0, count);
    for (const neighbor of nearest) {
      const cyclePeriod = rangeValue(rng, MIN_LINK_CYCLE_SECONDS, MAX_LINK_CYCLE_SECONDS);
      links.push({ from: index, to: neighbor.otherIndex, cycleOffset: rng() * cyclePeriod, cyclePeriod, phase: rng() * Math.PI * 2 });
    }
  }
  return { ...size, nodes, links, landDots: buildLandDots(size, rng) };
}

function sizeCanvas(canvas: HTMLCanvasElement, parent: HTMLElement): SizedScene | null {
  const rect = parent.getBoundingClientRect();
  const size = { width: Math.max(1, rect.width), height: Math.max(1, rect.height) };
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.floor(size.width * dpr);
  canvas.height = Math.floor(size.height * dpr);
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  // DPR scaling keeps the ambient lines crisp while drawing in CSS pixels.
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, scene: buildScene(size) };
}

function driftFor(time: number, staticFrame: boolean): Point {
  if (staticFrame) return { x: 0, y: 0 };
  return { x: Math.sin(time * 0.036) * MAP_DRIFT_PIXELS, y: Math.cos(time * 0.029) * MAP_DRIFT_PIXELS * 0.65 };
}

function shifted(point: Point, drift: Point): Point {
  return { x: point.x + drift.x, y: point.y + drift.y };
}

function nodeState(node: AgentNode, time: number, staticFrame: boolean): NodeState {
  const pulse = 0.5 + 0.5 * Math.sin(time * 1.15 + node.phase);
  if (staticFrame) return { alpha: 1, bloom: 0.45, pulse };
  const cycleTime = wrappedTime(time, node.cycleOffset, node.cyclePeriod);
  if (cycleTime < NODE_IGNITE_SECONDS) {
    const progress = easeOutCubic(cycleTime / NODE_IGNITE_SECONDS);
    return { alpha: progress, bloom: 1 - progress, pulse };
  }
  if (cycleTime < NODE_IGNITE_SECONDS + node.holdSeconds) return { alpha: 1, bloom: 0, pulse };
  if (cycleTime < NODE_IGNITE_SECONDS + node.holdSeconds + NODE_FADE_SECONDS) {
    const progress = (cycleTime - NODE_IGNITE_SECONDS - node.holdSeconds) / NODE_FADE_SECONDS;
    return { alpha: 1 - easeInOutCubic(progress), bloom: 0, pulse };
  }
  return { alpha: 0, bloom: 0, pulse };
}

function linkState(link: AgentLink, time: number, staticFrame: boolean) {
  if (staticFrame) return { progress: 1, alpha: 0.7 };
  const cycleTime = wrappedTime(time, link.cycleOffset, link.cyclePeriod);
  if (cycleTime >= LINK_DRAW_SECONDS + LINK_HOLD_SECONDS + LINK_FADE_SECONDS) return { progress: 0, alpha: 0 };
  if (cycleTime < LINK_DRAW_SECONDS) {
    const progress = easeInOutCubic(cycleTime / LINK_DRAW_SECONDS);
    return { progress, alpha: 0.45 + progress * 0.35 };
  }
  if (cycleTime < LINK_DRAW_SECONDS + LINK_HOLD_SECONDS) return { progress: 1, alpha: 0.72 };
  const fadeProgress = (cycleTime - LINK_DRAW_SECONDS - LINK_HOLD_SECONDS) / LINK_FADE_SECONDS;
  return { progress: 1, alpha: 0.72 * (1 - easeInOutCubic(fadeProgress)) };
}

function drawMap({ ctx, palette, scene, time, staticFrame }: Frame) {
  const drift = driftFor(time, staticFrame);
  ctx.save();
  ctx.fillStyle = palette.subtle;
  for (const dot of scene.landDots) {
    ctx.globalAlpha = dot.alpha;
    ctx.beginPath();
    ctx.arc(dot.x + drift.x, dot.y + drift.y, dot.radius, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.restore();
}

function arcControl(from: Point, to: Point, phase: number): Point {
  const dx = to.x - from.x;
  const dy = to.y - from.y;
  const length = Math.max(Math.hypot(dx, dy), 1);
  const bow = Math.min(128, Math.max(34, length * 0.24));
  const side = phase > Math.PI ? 1 : -1;
  return { x: (from.x + to.x) / 2 + (-dy / length) * bow * side, y: (from.y + to.y) / 2 + (dx / length) * bow * side };
}

function quadraticPoint(from: Point, control: Point, to: Point, t: number): Point {
  const inv = 1 - t;
  return {
    x: inv * inv * from.x + 2 * inv * t * control.x + t * t * to.x,
    y: inv * inv * from.y + 2 * inv * t * control.y + t * t * to.y,
  };
}

function drawArcSegment(ctx: CanvasRenderingContext2D, from: Point, control: Point, to: Point, progress: number) {
  const steps = Math.max(8, Math.ceil(progress * 28));
  ctx.beginPath();
  ctx.moveTo(from.x, from.y);
  for (let step = 1; step <= steps; step += 1) {
    const point = quadraticPoint(from, control, to, (progress * step) / steps);
    ctx.lineTo(point.x, point.y);
  }
  ctx.stroke();
}

function drawLinks({ ctx, scene, time, staticFrame }: Frame) {
  const drift = driftFor(time, staticFrame);
  ctx.save();
  ctx.lineWidth = 1;
  for (const link of scene.links) {
    const source = scene.nodes[link.from];
    const target = scene.nodes[link.to];
    if (!source || !target) continue;
    const sourceState = nodeState(source, time, staticFrame);
    const targetState = nodeState(target, time, staticFrame);
    const endpointAlpha = Math.min(sourceState.alpha, targetState.alpha);
    if (endpointAlpha <= 0.08) continue;
    const active = linkState(link, time, staticFrame);
    if (active.alpha <= 0 || active.progress <= 0) continue;
    const from = shifted(source, drift);
    const to = shifted(target, drift);
    const control = arcControl(from, to, link.phase);
    ctx.strokeStyle = withAlpha(source.color.link, (0.07 + 0.16 * active.alpha) * endpointAlpha);
    drawArcSegment(ctx, from, control, to, active.progress);
    for (const packetOffset of [0, 0.43]) {
      const packet = (time * 0.19 + link.phase + packetOffset) % 1;
      if (packet > active.progress || packet > 0.92) continue;
      const point = quadraticPoint(from, control, to, packet);
      const packetAlpha = active.alpha * endpointAlpha * easeInOutCubic(1 - packet / 0.92);
      ctx.fillStyle = withAlpha(source.color.glow, 0.42 * packetAlpha);
      ctx.beginPath();
      ctx.arc(point.x, point.y, 1.55, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.restore();
}

function drawNodes({ ctx, palette, scene, time, staticFrame }: Frame) {
  const drift = driftFor(time, staticFrame);
  ctx.save();
  for (const node of scene.nodes) {
    const state = nodeState(node, time, staticFrame);
    if (state.alpha <= 0.02) continue;
    const point = shifted(node, drift);
    const glow = ctx.createRadialGradient(point.x, point.y, 0, point.x, point.y, 14 + state.bloom * 18);
    glow.addColorStop(0, withAlpha(node.color.glow, (0.24 + state.pulse * 0.08) * state.alpha));
    glow.addColorStop(1, withAlpha(node.color.glow, 0));
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(point.x, point.y, 15 + state.bloom * 17, 0, Math.PI * 2);
    ctx.fill();
    ctx.strokeStyle = withAlpha(node.color.glow, 0.34 * state.bloom * state.alpha);
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.arc(point.x, point.y, 4 + state.bloom * 18, 0, Math.PI * 2);
    ctx.stroke();
    ctx.fillStyle = withAlpha(palette.surface, 0.95);
    ctx.beginPath();
    ctx.arc(point.x, point.y, 3.9, 0, Math.PI * 2);
    ctx.fill();
    ctx.fillStyle = withAlpha(node.color.core, (0.68 + state.pulse * 0.18) * state.alpha);
    ctx.beginPath();
    ctx.arc(point.x, point.y, 2.15 + state.pulse * 0.36, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.restore();
}

function draw(frame: Frame) {
  frame.ctx.clearRect(0, 0, frame.scene.width, frame.scene.height);
  drawMap(frame);
  drawLinks(frame);
  drawNodes(frame);
}

export function LoginBackground({ className }: { className?: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    const parent = canvas?.parentElement;
    if (!canvas || !parent) return;
    let frameId = 0;
    let lastTime = performance.now();
    let elapsed = 0;
    let palette = readPalette();
    let sized = sizeCanvas(canvas, parent);
    const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
    let reducedMotion = motionQuery.matches;
    const cancelFrame = () => { if (frameId !== 0) window.cancelAnimationFrame(frameId); frameId = 0; };
    const renderStatic = () => { if (sized) draw({ ctx: sized.ctx, palette, scene: sized.scene, time: 20, staticFrame: true }); };
    const tick = (now: number) => {
      if (!sized || reducedMotion || document.visibilityState === "hidden") return;
      elapsed += Math.min((now - lastTime) / 1000, MAX_DELTA_SECONDS);
      lastTime = now;
      draw({ ctx: sized.ctx, palette, scene: sized.scene, time: elapsed, staticFrame: false });
      frameId = window.requestAnimationFrame(tick);
    };
    const start = () => { cancelFrame(); lastTime = performance.now(); if (reducedMotion) renderStatic(); else frameId = window.requestAnimationFrame(tick); };
    const resize = () => { palette = readPalette(); sized = sizeCanvas(canvas, parent); if (reducedMotion) renderStatic(); };
    const onMotionChange = (event: MediaQueryListEvent) => { reducedMotion = event.matches; start(); };
    const onVisibilityChange = () => { if (document.visibilityState === "hidden") cancelFrame(); else start(); };
    const observer = new ResizeObserver(resize);
    observer.observe(parent); motionQuery.addEventListener("change", onMotionChange); document.addEventListener("visibilitychange", onVisibilityChange); start();
    return () => { cancelFrame(); observer.disconnect(); motionQuery.removeEventListener("change", onMotionChange); document.removeEventListener("visibilitychange", onVisibilityChange); };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      aria-hidden="true"
      className={cn("pointer-events-none absolute inset-0 h-full w-full", className)}
      style={{
        maskImage: "radial-gradient(72rem 46rem at 50% 46%, black 0%, rgba(0,0,0,0.94) 52%, rgba(0,0,0,0.6) 78%, rgba(0,0,0,0.26) 93%, transparent 100%)",
        WebkitMaskImage: "radial-gradient(72rem 46rem at 50% 46%, black 0%, rgba(0,0,0,0.94) 52%, rgba(0,0,0,0.6) 78%, rgba(0,0,0,0.26) 93%, transparent 100%)",
      }}
    />
  );
}
