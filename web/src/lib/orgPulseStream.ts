// Server-sent events for the organization board. The stream is read with fetch
// (authedFetch), so the session's bearer token travels in a header, as on every
// other request. EventSource cannot send headers, and the token must not go
// into a URL.

export type SseEvent = { readonly event: string; readonly data: string };

// parseSse splits complete events off the front of buf and returns them with
// the unfinished rest, which the next chunk completes. A block with no data
// lines is not an event.
export function parseSse(buf: string): { events: SseEvent[]; rest: string } {
  const events: SseEvent[] = [];
  let rest = buf;
  for (;;) {
    const at = rest.indexOf("\n\n");
    if (at < 0) break;
    const block = rest.slice(0, at);
    rest = rest.slice(at + 2);
    let event = "message";
    const data: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
    }
    if (data.length) events.push({ event, data: data.join("\n") });
  }
  return { events, rest };
}

// readSse feeds each event of a streamed body to onEvent until the body ends or
// signal aborts. It resolves when the body ends.
export async function readSse(
  body: ReadableStream<Uint8Array>,
  onEvent: (e: SseEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const reader = body.getReader();
  const cancel = () => void reader.cancel().catch(() => undefined);
  signal?.addEventListener("abort", cancel, { once: true });
  const dec = new TextDecoder();
  let buf = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const parsed = parseSse(buf);
      buf = parsed.rest;
      for (const e of parsed.events) onEvent(e);
    }
  } finally {
    signal?.removeEventListener("abort", cancel);
  }
}
