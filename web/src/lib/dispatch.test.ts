import { dispatchAttribution, dispatchLine } from "./dispatch.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const payload = {
  userId: "maya",
  text: "review the parser change",
  recipient: { ownerId: "bob", provider: "claude", core: "acme" },
  dispatchedBy: { jobId: "job_parent", depth: 2, via: "jack/codex" },
};
const d = dispatchAttribution(payload);
eq(d, { jobId: "job_parent", depth: 2, via: "jack/codex", to: "bob/claude" }, "marker with recipient");
eq(dispatchLine("maya", d!), "maya → via jack/codex → bob/claude", "chain line");
eq(
  dispatchLine("maya", dispatchAttribution({ ...payload, expert: { id: "cc_rev", name: "reviewer", version: "1.0.0" } })!),
  "maya → via jack/codex → bob/claude (as reviewer)",
  "expert",
);
eq(dispatchAttribution({ userId: "maya", text: "typed" }), undefined, "typed message");
eq(dispatchAttribution({ dispatchedBy: { depth: 1 } }), undefined, "marker without a job");
eq(dispatchAttribution({ dispatchedBy: { jobId: "j" } }), { jobId: "j", depth: 1, via: "an agent", to: undefined, expert: undefined }, "sparse marker");
eq(dispatchAttribution(null), undefined, "no payload");
