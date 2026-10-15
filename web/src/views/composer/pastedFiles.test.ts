import { pastedFiles } from "./pastedFiles.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const file = (name: string, type: string) => new File(["x"], name, { type });
const csv = file("eval-cases.csv", "text/csv");
const shot = file("image.png", "image/png");
const transfer = (files: File[], items: { kind: string; file?: File }[] = []) =>
  ({ files, items: items.map((i) => ({ kind: i.kind, getAsFile: () => i.file ?? null })) }) as unknown as DataTransfer;

// A file copied in the Finder is attached whatever its type (not only images).
eq(pastedFiles(transfer([csv])).map((f) => f.name), ["eval-cases.csv"], "a copied CSV");
eq(pastedFiles(transfer([shot, csv])).map((f) => f.name), ["image.png", "eval-cases.csv"], "several files");
// An engine that fills only the items.
eq(pastedFiles(transfer([], [{ kind: "string" }, { kind: "file", file: csv }])).map((f) => f.name), ["eval-cases.csv"], "items only");
// Plain text pastes as text.
eq(pastedFiles(transfer([], [{ kind: "string" }])), [], "text");
eq(pastedFiles(null), [], "no data");
