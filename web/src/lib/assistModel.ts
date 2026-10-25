import { cleanTags, splitList, tagProblem } from "./contentComponents.ts";
import {
  AssistTaskDescribeRoom,
  AssistTaskDescribeWork,
  AssistTaskEnhancePrompt,
  MaxAssistProblemRunes,
  MaxAssistProblems,
  MaxAssistRounds,
  MaxAssistText,
  WorkCategoryValues,
  type AssistJSONReply,
  type AssistRequest,
  type AssistRevise,
  type AssistStatus,
  type AssistTask,
  type WorkCategory,
} from "./wire.gen.ts";
import { workNameFrom, type PublishWorkForm } from "./workGallery.ts";

// The pure side of AI assist (docs/prd/2026-10-13-ai-assist.md §5; the
// requests are lib/assist.ts): the task names, a task in the
// status, the Server-Sent Events parser of a text task's stream, and a
// describe-work reply onto the publish form. Unit-tested.

/** The tasks the web's entries run (resolved by name: an organization or
 *  member may restyle them; their output contracts are
 *  wire.AssistContracts). */
export const ENHANCE_PROMPT = AssistTaskEnhancePrompt;
export const DESCRIBE_WORK = AssistTaskDescribeWork;
export const DESCRIBE_ROOM = AssistTaskDescribeRoom;

/** assistTask finds a runnable task in a status: null when assists are
 *  unavailable or the task does not exist. */
export function assistTask(status: AssistStatus | null | undefined, name: string): AssistTask | null {
  if (!status?.available) return null;
  return status.tasks.find((t) => t.name === name) ?? null;
}

// ---- Checking its work (docs/prd/2026-10-25-ai-checks-its-work.md) ----

/** A checked JSON reply: the fields, and the problems its check still
 *  finds (empty: it passed). */
export interface CheckedReply {
  readonly reply: AssistJSONReply;
  readonly problems: readonly string[];
}

/** assistRevision is the revision a failed check asks for: the previous
 *  answer (its fields as JSON, cut to the request's bound) and the
 *  problems as one line each, within their bounds; null when none is
 *  left. */
export function assistRevision(fields: AssistJSONReply["fields"], problems: readonly string[]): AssistRevise | null {
  const lines: string[] = [];
  for (const p of problems) {
    const line = p.split(/\s+/).filter(Boolean).join(" ");
    const chars = [...line];
    if (line && lines.length < MaxAssistProblems) lines.push(chars.length > MaxAssistProblemRunes ? chars.slice(0, MaxAssistProblemRunes - 1).join("") + "…" : line);
  }
  if (lines.length === 0) return null;
  return { answer: [...JSON.stringify(fields)].slice(0, MaxAssistText).join(""), problems: lines };
}

/** refineJSON is the web's one loop of an answer that checks its work: run
 *  the task, check the fields (the form's own rules), and while problems
 *  remain ask again with the previous answer and them, MaxAssistRounds in
 *  all. A revision that fails keeps the last answer: it never comes back
 *  as passing when it did not. */
export async function refineJSON(
  run: (body: AssistRequest) => Promise<AssistJSONReply>,
  body: AssistRequest,
  check: (fields: AssistJSONReply["fields"]) => readonly string[],
): Promise<CheckedReply> {
  let reply = await run(body);
  let problems = check(reply.fields);
  for (let round = 1; round < MaxAssistRounds && problems.length > 0; round++) {
    const revise = assistRevision(reply.fields, problems);
    if (!revise) break;
    try {
      reply = await run({ ...body, revise });
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") throw e;
      break;
    }
    problems = check(reply.fields);
  }
  return { reply, problems };
}

// ---- Server-Sent Events ----

/** One Server-Sent Event: its type ("message" when unnamed) and data. */
export interface SSEEvent {
  readonly event: string;
  readonly data: string;
}

/** createSSEParser reads an event stream in chunks of any size: lines end
 *  in LF, CRLF or CR (a CR at a chunk's end waits for the next chunk), a
 *  blank line dispatches, multi-line data joins with LF, ":" lines are
 *  comments. end() dispatches nothing incomplete (the stream was cut). */
export function createSSEParser(onEvent: (e: SSEEvent) => void): { push(chunk: string): void; end(): void } {
  let buf = "";
  let event = "";
  let data: string[] = [];
  const line = (l: string) => {
    if (l === "") {
      if (data.length > 0) onEvent({ event: event || "message", data: data.join("\n") });
      event = "";
      data = [];
      return;
    }
    if (l.startsWith(":")) return;
    const colon = l.indexOf(":");
    const field = colon < 0 ? l : l.slice(0, colon);
    let value = colon < 0 ? "" : l.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") event = value;
    else if (field === "data") data.push(value);
  };
  const drain = (final: boolean) => {
    for (;;) {
      const i = buf.search(/[\r\n]/);
      if (i < 0) return;
      if (buf[i] === "\r" && i === buf.length - 1 && !final) return;
      const end = buf[i] === "\r" && buf[i + 1] === "\n" ? i + 2 : i + 1;
      line(buf.slice(0, i));
      buf = buf.slice(end);
    }
  };
  return {
    push(chunk: string) {
      buf += chunk;
      drain(false);
    },
    end() {
      drain(true);
    },
  };
}

// ---- describe-work onto the publish form ----

const MAX_TAGS = 12;
const MAX_DESCRIPTION = 500;

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** workDraftPatch turns a describe-work reply into the publish form's
 *  fields: a category the gallery has (anything else is dropped), the tags
 *  the tag rule admits (at most 12), the description within the form's
 *  bound. The name follows the display name only while the member has
 *  not changed it, and only when the display name gives one. */
export function workDraftPatch(fields: Readonly<Record<string, unknown>>, form: PublishWorkForm): Partial<PublishWorkForm> {
  const patch: { -readonly [K in keyof PublishWorkForm]?: PublishWorkForm[K] } = {};
  const displayName = str(fields.displayName);
  if (displayName) {
    patch.displayName = displayName;
    const suggested = workNameFrom(displayName);
    if (suggested && form.name === workNameFrom(form.displayName)) patch.name = suggested;
  }
  const description = str(fields.description);
  if (description) patch.description = description.slice(0, MAX_DESCRIPTION);
  const category = str(fields.category);
  if ((WorkCategoryValues as readonly string[]).includes(category)) patch.category = category as WorkCategory;
  const raw = Array.isArray(fields.tags) ? fields.tags.filter((t): t is string => typeof t === "string") : splitList(str(fields.tags));
  const tags = cleanTags(raw)
    .filter((t) => tagProblem([t]) === null)
    .slice(0, MAX_TAGS);
  if (tags.length > 0) patch.tags = tags.join(", ");
  return patch;
}
