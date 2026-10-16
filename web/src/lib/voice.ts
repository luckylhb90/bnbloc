// Voice input (docs/prd/2026-10-16-voice-and-image-generation.md §3): the
// microphone's recording, its resample to 16 kHz mono PCM and its WAV
// encoding, written here on the standard Web Audio APIs — no library, no
// other product's code. The routes the transcript comes back through are
// lib/media.ts (the one owner of the media requests, the boundary rule).

/** The recording states: idle, capturing, encoding, sending. */
export type VoiceState = "idle" | "recording" | "encoding" | "transcribing";

/** A microphone capture: start opens the microphone, stop ends it and
 *  resolves the encoded WAV. */
export interface VoiceRecorder {
  start(): Promise<void>;
  stop(): Promise<Blob>;
  stopNow(): void;
}

/** The auto-stop cap of a recording: 120 s of 16 kHz mono PCM is ~3.8 MiB,
 *  far under the route's cap (wire.MaxMediaAudioBytes). */
export const VOICE_MAX_MS = 120_000;

/** Opens the microphone and records until stop, auto-stopping at
 *  VOICE_MAX_MS. The resolved blob is 16 kHz mono PCM WAV when the browser
 *  could decode its capture; the raw capture otherwise (the provider's
 *  ears do the resampling then). */
export function openVoiceRecorder(): VoiceRecorder {
  let recorder: MediaRecorder | null = null;
  let stream: MediaStream | null = null;
  let chunks: BlobPart[] = [];
  let timer = 0;
  let settle: ((b: Blob) => void) | null = null;

  async function start() {
    if (recorder) throw new Error("Already recording");
    stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const mime = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"].find((t) => MediaRecorder.isTypeSupported?.(t));
    recorder = new MediaRecorder(stream, mime ? { mimeType: mime } : undefined);
    chunks = [];
    recorder.ondataavailable = (e) => {
      if (e.data.size > 0) chunks.push(e.data);
    };
    recorder.start(250);
    timer = window.setTimeout(() => stopNow(), VOICE_MAX_MS);
  }

  function stopNow() {
    window.clearTimeout(timer);
    if (recorder && recorder.state !== "inactive") recorder.stop();
  }

  function stop(): Promise<Blob> {
    return new Promise((resolve, reject) => {
      if (!recorder) {
        reject(new Error("Not recording"));
        return;
      }
      const r = recorder;
      settle = (b) => resolve(b);
      r.onstop = () => {
        stream?.getTracks().forEach((t) => t.stop());
        const raw = new Blob(chunks, { type: r.mimeType || "audio/webm" });
        toWav(raw)
          .then(resolve, () => resolve(raw)) // a decode failure: send what was captured
          .finally(() => {
            recorder = null;
            stream = null;
          });
      };
      stopNow();
    });
  }

  return {
    start,
    stop,
    stopNow: () => {
      void settle;
      stopNow();
    },
  };
}

/** Decodes the captured audio and resamples it to 16 kHz mono PCM, encoded
 *  as a WAV file (a 44-byte RIFF header, 16-bit little-endian samples). */
async function toWav(blob: Blob): Promise<Blob> {
  const bytes = await blob.arrayBuffer();
  const ctx = new AudioContext();
  let decoded: AudioBuffer;
  try {
    decoded = await ctx.decodeAudioData(bytes.slice(0));
  } finally {
    void ctx.close();
  }
  const rate = 16_000;
  const frames = Math.max(1, Math.ceil((decoded.length * rate) / decoded.sampleRate));
  const offline = new OfflineAudioContext(1, frames, rate);
  const src = offline.createBufferSource();
  src.buffer = decoded;
  // OfflineAudioContext downmixes every input channel into the one mono
  // output channel (equal-power-ish averaging per the specification).
  src.connect(offline.destination);
  src.start();
  const rendered = await offline.startRendering();
  const data = rendered.getChannelData(0);
  const out = new DataView(new ArrayBuffer(44 + data.length * 2));
  writeWavHeader(out, data.length);
  let o = 44;
  for (let i = 0; i < data.length; i++) {
    const s = Math.max(-1, Math.min(1, data[i]));
    out.setInt16(o, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    o += 2;
  }
  return new Blob([out.buffer], { type: "audio/wav" });
}

/** Writes the 44-byte RIFF/WAVE header of a mono 16-bit 16 kHz PCM file. */
export function writeWavHeader(out: DataView, samples: number) {
  const bytes = samples * 2;
  const str = (o: number, s: string) => {
    for (let i = 0; i < s.length; i++) out.setUint8(o + i, s.charCodeAt(i));
  };
  str(0, "RIFF");
  out.setUint32(4, 36 + bytes, true);
  str(8, "WAVEfmt ");
  out.setUint32(16, 16, true); // fmt chunk size
  out.setUint16(20, 1, true); // PCM
  out.setUint16(22, 1, true); // mono
  out.setUint32(24, 16_000, true); // sample rate
  out.setUint32(28, 32_000, true); // byte rate
  out.setUint16(32, 2, true); // block align
  out.setUint16(34, 16, true); // bits per sample
  str(36, "data");
  out.setUint32(40, bytes, true);
}
