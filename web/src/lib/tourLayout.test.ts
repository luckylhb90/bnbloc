import { placeCoach, spotlightRect } from "./tourLayout.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const desk = { width: 1280, height: 800 };
const phone = { width: 390, height: 800 };
const card = { width: 340, height: 180 };

function testSpotlight(): void {
  const s = spotlightRect({ top: 10, left: 300, width: 600, height: 60 }, desk);
  assert(s !== null && s.top === 4 && s.left === 294 && s.width === 612 && s.height === 72, `padded and clipped: ${JSON.stringify(s)}`);
  const tall = spotlightRect({ top: 60, left: 0, width: 1280, height: 4000 }, desk);
  assert(tall !== null && tall.top + tall.height <= desk.height && tall.left >= 0, "a tall feed is clipped to the screen");
  assert(spotlightRect({ top: 900, left: 0, width: 100, height: 40 }, desk) === null, "off screen: no cutout");
}

function testFloat(): void {
  const header = placeCoach({ top: 4, left: 236, width: 1040, height: 64 }, card, desk);
  assert(header.mode === "float" && header.top === 80, `below the header: ${JSON.stringify(header)}`);
  const composer = placeCoach({ top: 640, left: 236, width: 1040, height: 150 }, card, desk);
  assert(composer.mode === "float" && composer.top === 640 - 12 - 180, "above the composer");
  const feed = placeCoach({ top: 80, left: 236, width: 1040, height: 560 }, card, desk);
  assert(feed.mode === "float" && feed.top + card.height <= desk.height, "never off screen");
  const rail = placeCoach({ top: 4, left: 236, width: 120, height: 792 }, card, desk);
  assert(rail.mode === "float" && rail.left === 236 + 120 + 12, "right of a full-height rail");
  const full = placeCoach({ top: 4, left: 4, width: 1272, height: 792 }, card, desk);
  assert(full.mode === "float" && full.top === 4 + 792 - 180 - 24, "inside the lower edge of a full-screen target");
  const none = placeCoach(null, card, desk);
  assert(none.mode === "float" && none.left === (1280 - 340) / 2, "centered without a target");
  const corner = placeCoach({ top: 100, left: 1250, width: 20, height: 20 }, card, desk);
  assert(corner.mode === "float" && corner.left + card.width <= desk.width - 8, "clamped horizontally");
}

function testSheet(): void {
  const sheetCard = { width: 390, height: 200 };
  assert(JSON.stringify(placeCoach(null, sheetCard, phone)) === '{"mode":"sheet","edge":"bottom"}', "phone: bottom sheet");
  const header = placeCoach({ top: 0, left: 0, width: 390, height: 110 }, sheetCard, phone);
  assert(header.mode === "sheet" && header.edge === "bottom", "header: bottom sheet");
  const composer = placeCoach({ top: 600, left: 0, width: 390, height: 196 }, sheetCard, phone);
  assert(composer.mode === "sheet" && composer.edge === "top", "the composer is not covered: sheet moves to the top");
  const feed = placeCoach({ top: 110, left: 0, width: 390, height: 490 }, sheetCard, phone);
  assert(feed.mode === "sheet" && feed.edge === "bottom", "a screen-filling feed keeps the bottom sheet");
}

testSpotlight();
testFloat();
testSheet();
