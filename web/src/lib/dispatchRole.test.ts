import { dispatchAttribution, dispatchLine } from "./dispatch.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// A team lead's dispatch is labeled with the role it staffs.
const payload = {
  userId: "maya",
  recipient: { ownerId: "bob", provider: "claude" },
  dispatchedBy: { jobId: "job_lead", depth: 1, via: "jack/codex", role: "reviewer" },
  expert: { id: "cc_rev", name: "code-reviewer", displayName: "Code reviewer", version: "1.2.0" },
};
const d = dispatchAttribution(payload);
eq(d?.role, "reviewer", "role read");
eq(dispatchLine("maya", d!), "maya → via jack/codex → bob/claude · reviewer (as Code reviewer)", "role line");
eq(dispatchAttribution({ dispatchedBy: { jobId: "j", role: 7 } })?.role, undefined, "a malformed role is ignored");
