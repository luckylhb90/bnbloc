// Media model calls (docs/prd/2026-10-16-voice-and-image-generation.md):
// the one owner of the media requests in the web (the boundary rule beside
// B2): what runs for the caller (GET /v1/media), a transcription (POST
// /v1/media/transcriptions) and an image generation (POST /v1/media/image).
// No other file names a media route. The microphone and the WAV encoder are
// lib/voice.ts.

import { useEffect, useState } from "react";
import { req } from "./api.ts";
import { useAuth } from "./auth";
import type { MediaCapabilities, MediaImageAspect, MediaImageReply } from "./wire.gen.ts";

const TTL_MS = 5 * 60 * 1000;
let cached: { tenant: string; at: number; caps: Promise<MediaCapabilities> } | null = null;
const listeners = new Set<() => void>();

function loadCaps(tenant: string | null): Promise<MediaCapabilities> {
  const key = tenant ?? "";
  if (cached && cached.tenant === key && Date.now() - cached.at < TTL_MS) return cached.caps;
  const caps = req<MediaCapabilities>("/v1/media");
  const entry = { tenant: key, at: Date.now(), caps };
  cached = entry;
  caps.catch(() => {
    if (cached === entry) cached = null;
  });
  return caps;
}

/** Drops the cached capabilities and re-reads them in every mounted
 *  consumer (an admin configured a provider, an org turned AI off). */
export function invalidateMediaCaps(): void {
  cached = null;
  for (const l of listeners) l();
}

/** Whether the caller can transcribe: null while loading or on a failure
 *  (the entry hides). */
export function useMediaCapability(cap: "transcribe" | "image"): boolean | null {
  const { tenant } = useAuth();
  const [rev, setRev] = useState(0);
  const [caps, setCaps] = useState<MediaCapabilities | null>(null);
  useEffect(() => {
    const listener = () => setRev((n) => n + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  useEffect(() => {
    let cancelled = false;
    loadCaps(tenant)
      .then((c) => {
        if (!cancelled) setCaps(c);
      })
      .catch(() => {
        if (!cancelled) setCaps(null);
      });
    return () => {
      cancelled = true;
    };
  }, [tenant, rev]);
  if (!caps) return null;
  return cap === "transcribe" ? caps.transcribe.available : caps.image.available;
}

/** Transcribes one audio blob (16 kHz mono WAV from lib/voice.ts) into text. */
export function transcribeAudio(blob: Blob, filename = "voice.wav", signal?: AbortSignal): Promise<{ text: string }> {
  return req<{ text: string }>(`/v1/media/transcriptions?filename=${encodeURIComponent(filename)}`, {
    method: "POST",
    headers: { "Content-Type": blob.type || "audio/wav" },
    body: blob,
    signal,
  });
}

/** Generates one image. */
export function generateImage(prompt: string, aspect: MediaImageAspect = "square", signal?: AbortSignal): Promise<MediaImageReply> {
  return req<MediaImageReply>("/v1/media/image", { method: "POST", body: JSON.stringify({ prompt, aspect }), signal });
}

// ---- The platform console's media providers ----

/** One capability's provider as the console edits it. */
export interface MediaProviderConfigView {
  configurable: boolean;
  kind: string;
  baseUrl: string;
  model: string;
  keySet: boolean;
  params?: Record<string, string>;
  updatedBy?: string;
  updatedAtMs?: number;
}

/** A provider update (PUT): an empty apiKey keeps the stored one. */
export interface MediaProviderUpdateBody {
  kind: string;
  baseUrl: string;
  model: string;
  apiKey?: string;
  clearKey?: boolean;
  params?: Record<string, string>;
}

/** The console's provider API per capability (never the key). */
export function mediaProviderApi(cap: "transcribe" | "image") {
  const base = `/v1/platform/media/${cap}`;
  return {
    get: () => req<MediaProviderConfigView>(base),
    put: (body: MediaProviderUpdateBody) =>
      req<MediaProviderConfigView>(base, { method: "PUT", body: JSON.stringify(body) }).then((c) => {
        invalidateMediaCaps();
        return c;
      }),
    test: () => req<{ ok: boolean; error?: string; reply?: string; latencyMs: number }>(`${base}/test`, { method: "POST" }),
  };
}

/** The adapter kinds the console offers (kinds are the platform's wire
 *  protocol families; openai-compatible is the first). */
export const MEDIA_ADAPTER_KINDS = ["openai-compatible"] as const;
