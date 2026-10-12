// Display names (PRD 2026-10-10 §1–2). A user id is the identity — usually an
// e-mail address — and stays the identity for mentions, recipients,
// permissions and URLs; what the UI renders is the person's display name. The
// server applies the same default rule (`GET /v1/users/profiles`), so the
// client-side default below is what shows until (or if) a lookup answers: no
// surface ever renders a raw address.
//
// Pure logic only (default rule, handle formatting, masking, the batching
// resolver); the React binding is useDisplayName.ts.

import { MaxDisplayNameRunes } from "./wire.gen.ts";

export interface Profile {
  readonly userId: string;
  readonly displayName: string;
}

// defaultDisplayName is the local part of the id (before '@'), or the id
// itself when it has no '@' (or the local part would be empty).
export function defaultDisplayName(id: string): string {
  const trimmed = id.trim();
  const at = trimmed.indexOf("@");
  if (at > 0) return trimmed.slice(0, at);
  return trimmed;
}

// formatAgentHandle renders an agent as "<owner display name> · <provider>"
// (e.g. "Maya · claude-code"); the canonical "<ownerId>/<provider>" handle
// stays the routing key and is never shown.
export function formatAgentHandle(ownerName: string, provider: string): string {
  const name = ownerName.trim();
  const p = provider.trim();
  if (!name) return p;
  if (!p) return name;
  return `${name} · ${p}`;
}

// validateDisplayName mirrors the server rule: 1–40 chars after trimming, no
// control characters. An empty value is valid — it clears the name.
export function validateDisplayName(raw: string): string | null {
  const v = raw.trim();
  if ([...v].length > MaxDisplayNameRunes) return `Use at most ${MaxDisplayNameRunes} characters.`;
  if (/[\u0000-\u001f\u007f]/.test(v)) return "Remove control characters.";
  return null;
}

const EMAIL_RE = /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+/g;

export function looksLikeEmail(s: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.trim());
}

// maskEmail is what privacy mode shows in place of an address that is not a
// known user (an invite, a typed value shown back): the display-name default.
export function maskEmail(address: string): string {
  return defaultDisplayName(address);
}

// maskEmailsInText replaces every address inside free text (audit details,
// error strings) with its display-name default.
export function maskEmailsInText(text: string): string {
  return text.replace(EMAIL_RE, (m) => maskEmail(m));
}

export function nameKey(id: string): string {
  return id.trim();
}

export type FetchProfiles = (ids: readonly string[]) => Promise<readonly Profile[]>;

export interface NameResolverOptions {
  readonly fetchProfiles: FetchProfiles;
  // Defers a flush so lookups made in one render pass share a request.
  readonly schedule?: (flush: () => void) => void;
  readonly now?: () => number;
  // The endpoint answers at most this many ids per call.
  readonly maxBatch?: number;
  // A failed lookup (e.g. a gateway without the endpoint) is not retried for
  // this long; the default rule renders meanwhile.
  readonly retryMs?: number;
}

// NameResolver is one shared cache of display names with batched, deduped
// lookups: request() queues ids, the scheduled flush sends each distinct
// unknown id once (chunked at maxBatch), and subscribers are told when names
// land. peek() is synchronous and never returns an address.
export class NameResolver {
  private readonly names = new Map<string, string>();
  private readonly failedAt = new Map<string, number>();
  private readonly inFlight = new Set<string>();
  private queued = new Map<string, string>();
  private scheduled = false;
  private readonly listeners = new Set<() => void>();
  private versionValue = 0;
  private readonly opts: Required<NameResolverOptions>;

  constructor(opts: NameResolverOptions) {
    this.opts = {
      schedule: (flush) => setTimeout(flush, 10),
      now: () => Date.now(),
      maxBatch: 200,
      retryMs: 5 * 60_000,
      ...opts,
    };
  }

  get version(): number {
    return this.versionValue;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  };

  getVersion = (): number => this.versionValue;

  peek(id: string): string {
    return this.names.get(nameKey(id)) ?? defaultDisplayName(id);
  }

  has(id: string): boolean {
    return this.names.has(nameKey(id));
  }

  request(ids: Iterable<string>): void {
    for (const id of ids) {
      const key = nameKey(id);
      if (!key || this.names.has(key) || this.inFlight.has(key) || this.queued.has(key)) continue;
      const failed = this.failedAt.get(key);
      if (failed !== undefined && this.opts.now() - failed < this.opts.retryMs) continue;
      this.queued.set(key, id.trim());
    }
    if (this.queued.size > 0 && !this.scheduled) {
      this.scheduled = true;
      this.opts.schedule(() => void this.flush());
    }
  }

  // set records a name the client knows authoritatively (the member's own
  // save); null/empty restores the default.
  set(id: string, displayName: string | null): void {
    const key = nameKey(id);
    if (!key) return;
    const name = displayName?.trim() || defaultDisplayName(id);
    this.names.set(key, name);
    this.failedAt.delete(key);
    this.bump();
  }

  async flush(): Promise<void> {
    this.scheduled = false;
    const batch = [...this.queued.entries()];
    this.queued = new Map();
    if (batch.length === 0) return;
    for (const [key] of batch) this.inFlight.add(key);
    const chunks: [string, string][][] = [];
    for (let i = 0; i < batch.length; i += this.opts.maxBatch) chunks.push(batch.slice(i, i + this.opts.maxBatch));
    await Promise.all(chunks.map((chunk) => this.lookup(chunk)));
    this.bump();
  }

  private async lookup(chunk: [string, string][]): Promise<void> {
    try {
      const profiles = await this.opts.fetchProfiles(chunk.map(([, id]) => id));
      const got = new Map<string, string>();
      for (const p of profiles ?? []) {
        if (p?.userId) got.set(nameKey(p.userId), p.displayName?.trim() || defaultDisplayName(p.userId));
      }
      for (const [key, id] of chunk) {
        this.names.set(key, got.get(key) ?? defaultDisplayName(id));
        this.failedAt.delete(key);
      }
    } catch {
      const at = this.opts.now();
      for (const [key] of chunk) this.failedAt.set(key, at);
    } finally {
      for (const [key] of chunk) this.inFlight.delete(key);
    }
  }

  private bump(): void {
    this.versionValue++;
    for (const fn of this.listeners) fn();
  }
}

// agentDisplayLabel is an agent's full display label: "Maya · codex", with
// the core appended when the agent advertises one ("Maya · codex (corex)").
export function agentDisplayLabel(
  a: { readonly ownerId: string; readonly provider: string; readonly core?: string },
  nameOf: (id: string) => string = defaultDisplayName,
): string {
  const handle = formatAgentHandle(nameOf(a.ownerId), a.provider);
  return a.core ? `${handle} (${a.core})` : handle;
}
