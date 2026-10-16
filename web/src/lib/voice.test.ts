import { writeWavHeader, VOICE_MAX_MS } from "./voice.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(got: unknown, want: unknown, what: string): void {
  assert(JSON.stringify(got) === JSON.stringify(want), `${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

// The WAV encoder's golden bytes: a 44-byte RIFF/WAVE header of mono 16-bit
// 16 kHz PCM, its sizes consistent, its fields little-endian (voice input
// PRD §3). A provider parses this exact shape; drift is a wire break.
assert((() => {
  const view = new DataView(new ArrayBuffer(44));
  writeWavHeader(view, 3);
  const str = (o: number, n: number) => String.fromCharCode(...Array.from(new Uint8Array(view.buffer, o, n)));
  eq(str(0, 4), "RIFF", "the RIFF tag");
  eq(view.getUint32(4, true), 36 + 6, "the RIFF size (36 + data bytes)");
  eq(str(8, 4), "WAVE", "the WAVE tag");
  eq(str(12, 4), "fmt ", "the fmt tag");
  eq(view.getUint32(16, true), 16, "the fmt chunk size");
  eq(view.getUint16(20, true), 1, "PCM");
  eq(view.getUint16(22, true), 1, "mono");
  eq(view.getUint32(24, true), 16_000, "the sample rate");
  eq(view.getUint32(28, true), 32_000, "the byte rate");
  eq(view.getUint16(32, true), 2, "the block align");
  eq(view.getUint16(34, true), 16, "the bits per sample");
  eq(str(36, 4), "data", "the data tag");
  eq(view.getUint32(40, true), 6, "the data size");
  return true;
})(), "the canonical 16 kHz mono PCM header");

// A recording's auto-stop cap: 120 s of 16 kHz mono PCM is ~3.8 MiB, under
// the route's cap (MaxMediaAudioBytes, 20 MiB) with headroom for a slow
// upload and a provider's own limit.
assert(VOICE_MAX_MS === 120_000, "the cap is 120 s");
assert(44 + (VOICE_MAX_MS / 1000) * 16_000 * 2 < 20 * 1024 * 1024, "the capped WAV stays under the route's cap");
