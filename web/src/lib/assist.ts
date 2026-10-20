import { useEffect, useState } from "react";
import { req } from "./api.ts";
import { useAuth } from "./auth";
import { ApiError } from "./errors.ts";
import { assistTask, createSSEParser } from "./assistModel.ts";
import type {
  AssistDelta,
  AssistDone,
  AssistJSONReply,
  AssistModelConfig,
  AssistModelTest,
  AssistModelUpdate,
  AssistOrgModel,
  AssistRequest,
  AssistStatus,
  AssistTask,
  AssistUsage,
} from "./wire.gen.ts";

// AI assist (docs/prd/2026-10-13-ai-assist.md §5): the one owner of the
// assist requests in the web — the status (is there a model, which tasks
// the member can run; every assist entry is hidden without it), a text
// task's streamed reply (Server-Sent Events over an authenticated fetch:
// EventSource cannot POST), a JSON task's reply, and the platform
// console's and the organization's assist model. No other file names an assist route or holds a
// prompt (web boundary rule B2): the tasks and their instructions are
// assist content components on the server. The pure helpers (the SSE
// parser, a JSON reply onto the publish form) are lib/assistModel.ts.

export type { AssistModelConfig, AssistModelTest, AssistModelUpdate, AssistOrgModel, AssistTask, AssistUsage } from "./wire.gen.ts";
export { DESCRIBE_ROOM, DESCRIBE_WORK, ENHANCE_PROMPT, workDraftPatch } from "./assistModel.ts";

const enc = encodeURIComponent;

// ---- The status ----

const TTL_MS = 5 * 60 * 1000;
let cached: { tenant: string; at: number; status: Promise<AssistStatus> } | null = null;
const listeners = new Set<() => void>();

function loadAssistStatus(tenant: string | null): Promise<AssistStatus> {
  const key = tenant ?? "";
  if (cached && cached.tenant === key && Date.now() - cached.at < TTL_MS) return cached.status;
  const status = req<AssistStatus>("/v1/assist");
  const entry = { tenant: key, at: Date.now(), status };
  cached = entry;
  // A failure is not cached: the next consumer retries.
  status.catch(() => {
    if (cached === entry) cached = null;
  });
  return status;
}

/** Drops the cached status and re-reads it in every mounted consumer (an
 *  org admin turned assists on or off, a super admin set the model). */
export function invalidateAssistStatus(): void {
  cached = null;
  for (const l of listeners) l();
}

/** The task the member can run now; null while loading, when assists are
 *  unavailable or the task is missing (the entry is then hidden). */
export function useAssistTask(name: string): AssistTask | null {
  const { tenant } = useAuth();
  const [rev, setRev] = useState(0);
  const [status, setStatus] = useState<AssistStatus | null>(null);
  useEffect(() => {
    const listener = () => setRev((n) => n + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  useEffect(() => {
    let cancelled = false;
    loadAssistStatus(tenant)
      .then((s) => {
        if (!cancelled) setStatus(s);
      })
      .catch(() => {
        if (!cancelled) setStatus(null);
      });
    return () => {
      cancelled = true;
    };
  }, [tenant, rev]);
  return assistTask(status, name);
}

// ---- Running a task ----

/** withTimezone adds the member's timezone (the clock context) unless the
 *  request names one (a schedule in another timezone). */
function withTimezone(body: AssistRequest): string {
  if (body.timezone) return JSON.stringify(body);
  let timezone = "";
  try {
    timezone = Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    // An unknown timezone: the server uses UTC.
  }
  return JSON.stringify(timezone ? { ...body, timezone } : body);
}

/** A text task's reply: the whole text and the usage. */
export interface AssistTextResult {
  readonly text: string;
  readonly usage: AssistUsage;
}

function payload<T>(data: string): T {
  try {
    return JSON.parse(data) as T;
  } catch {
    throw new ApiError(502, "The assist sent a reply that could not be read");
  }
}

/** runTextAssist streams a text task's reply: onDelta gets the text so far
 *  after every piece. An abort through signal rejects with the abort. */
export async function runTextAssist(
  task: string,
  body: AssistRequest,
  opts: { readonly onDelta: (soFar: string) => void; readonly signal?: AbortSignal },
): Promise<AssistTextResult> {
  const res = await req<Response>(`/v1/assist/${enc(task)}`, { method: "POST", body: withTimezone(body), as: "response", signal: opts.signal });
  if (!res.body) throw new ApiError(502, "The assist sent no reply");
  let text = "";
  let usage: AssistUsage | null = null;
  let failure: string | null = null;
  const parser = createSSEParser((e) => {
    if (e.event === "delta") {
      text += payload<AssistDelta>(e.data).text ?? "";
      opts.onDelta(text);
    } else if (e.event === "done") {
      usage = payload<AssistDone>(e.data).usage;
    } else if (e.event === "error") {
      failure = payload<{ error?: string }>(e.data).error || "The assist failed";
    }
  });
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parser.push(decoder.decode(value, { stream: true }));
    if (failure || usage) break;
  }
  parser.push(decoder.decode());
  parser.end();
  if (failure) throw new ApiError(502, failure);
  if (!usage) throw new ApiError(502, "The assist stopped before it finished");
  return { text, usage };
}

/** runJSONAssist runs a JSON task: its declared fields. */
export function runJSONAssist(task: string, body: AssistRequest, signal?: AbortSignal): Promise<AssistJSONReply> {
  return req<AssistJSONReply>(`/v1/assist/${enc(task)}`, { method: "POST", body: withTimezone(body), signal });
}

/** isAbort reports a run the member stopped. */
export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

// ---- The platform console's assist model ----

export const assistModelApi = {
  get: () => req<AssistModelConfig>("/v1/platform/assist-model"),
  put: (body: AssistModelUpdate) =>
    req<AssistModelConfig>("/v1/platform/assist-model", { method: "PUT", body: JSON.stringify(body) }).then((c) => {
      invalidateAssistStatus();
      return c;
    }),
  test: () => req<AssistModelTest>("/v1/platform/assist-model/test", { method: "POST" }),
};

// ---- The organization's own model ----

/** An org admin's model (it replaces the platform's for the organization's
 *  members, on a host the platform allows); an empty base URL goes back
 *  to the platform's. */
export const orgAssistModelApi = {
  get: () => req<AssistOrgModel>("/v1/tenant/assist-model"),
  put: (body: AssistModelUpdate) =>
    req<AssistOrgModel>("/v1/tenant/assist-model", { method: "PUT", body: JSON.stringify(body) }).then((m) => {
      invalidateAssistStatus();
      return m;
    }),
  test: () => req<AssistModelTest>("/v1/tenant/assist-model/test", { method: "POST" }),
};
