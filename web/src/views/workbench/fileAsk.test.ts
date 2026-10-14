import { fileThread, requestSelection } from "./fileAsk.ts";
import type { FeedItem } from "../room/types.ts";

// Local runner (the web tsconfig has no node types); a failure throws.
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}
const row = (id: number, extra: Partial<FeedItem>): FeedItem => ({ id, author: "maya", text: `m${id}`, self: false, ...extra });

// The thread is the requests about the file and what answered them, in
// feed order: not other requests, not other files' replies.
const feed: FeedItem[] = [
  row(1, { seq: 10, fileRef: { artifactId: "a", version: 2 } }),
  row(2, { seq: 11, text: "unrelated" }),
  row(3, { seq: 12, author: "agent", sourceSeq: 10 }),
  row(4, { seq: 13, author: "agent", sourceSeq: 11 }),
  row(5, { seq: 14, fileRef: { artifactId: "b", version: 1 } }),
  row(6, { seq: 15, author: "agent", sourceSeq: 14 }),
  row(7, { seq: 16, author: "agent", sourceSeq: 10, notice: "file-edit" }),
  row(8, { fileRef: { artifactId: "a", version: 3 }, status: "pending" }),
];
eq(fileThread(feed, "a").map((m) => m.id), [1, 3, 7, 8], "file a's thread");
eq(fileThread(feed, "b").map((m) => m.id), [5, 6], "file b's thread");
eq(fileThread(feed, "c"), [], "no thread");
console.log("fileAsk tests passed");

// A request carries the selection's label, content, kind and keys; never
// where it showed on screen.
{
  const sent = requestSelection({ label: "Slide 2", detail: "Highlights", kind: "slide", rect: { x: 1, y: 2, w: 3, h: 4 }, keys: ["slide-257"] });
  if (JSON.stringify(sent) !== JSON.stringify({ label: "Slide 2", detail: "Highlights", kind: "slide", keys: ["slide-257"] })) throw new Error(`requestSelection: ${JSON.stringify(sent)}`);
}
