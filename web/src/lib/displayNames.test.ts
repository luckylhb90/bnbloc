import {
  NameResolver,
  agentDisplayLabel,
  defaultDisplayName,
  formatAgentHandle,
  looksLikeEmail,
  maskEmail,
  maskEmailsInText,
  validateDisplayName,
  type Profile,
} from "./displayNames.ts";
import { mentionLabel, mentionMatches } from "../views/composer/mentions.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testDefaultRule(): void {
  assert(defaultDisplayName("jane.d@example.com") === "jane.d", "local part before @");
  assert(defaultDisplayName("  jack@x.io ") === "jack", "trimmed");
  assert(defaultDisplayName("alice") === "alice", "an id without @ is itself");
  assert(defaultDisplayName("@weird") === "@weird", "an empty local part keeps the id");
}

function testHandle(): void {
  assert(formatAgentHandle("Maya", "claude-code") === "Maya · claude-code", "name · provider");
  assert(formatAgentHandle("", "codex") === "codex", "no owner name -> provider");
  assert(formatAgentHandle("Maya", "") === "Maya", "no provider -> name");
  assert(agentDisplayLabel({ ownerId: "jane.d@example.com", provider: "codex" }) === "jane.d · codex", "default owner name");
  assert(agentDisplayLabel({ ownerId: "h@x.io", provider: "codex", core: "corex" }, () => "Maya") === "Maya · codex (corex)", "core appended");
}

function testValidation(): void {
  assert(validateDisplayName("") === null, "empty clears");
  assert(validateDisplayName("  Maya  ") === null, "trimmed name is fine");
  assert(validateDisplayName("x".repeat(41)) !== null, "41 chars rejected");
  assert(validateDisplayName("a\u0007b") !== null, "control chars rejected");
}

function testMasking(): void {
  assert(looksLikeEmail("a@b.co") && !looksLikeEmail("alice") && !looksLikeEmail("a@b"), "email shape");
  assert(maskEmail("jane.d@example.com") === "jane.d", "masked address is its default name");
  const masked = maskEmailsInText("added jack@x.io and jill.k@corp.example.com as admins");
  assert(masked === "added jack and jill.k as admins", `free text masked: ${masked}`);
  assert(!/@[a-z0-9-]+\./i.test(masked), "no domain survives");
}

async function testBatchingAndDedupe(): Promise<void> {
  const calls: string[][] = [];
  let flush: (() => void) | null = null;
  const r = new NameResolver({
    fetchProfiles: async (ids) => {
      calls.push([...ids]);
      return ids.filter((id) => id.startsWith("a")).map((id): Profile => ({ userId: id.toUpperCase(), displayName: `N-${id}` }));
    },
    schedule: (f) => {
      flush = f;
    },
    maxBatch: 2,
  });
  let notified = 0;
  r.subscribe(() => notified++);
  assert(r.peek("a1@x.io") === "a1", "default before the lookup answers");
  r.request(["a1@x.io", "A1@x.io", "b2@x.io"]);
  r.request(["a3@x.io", "a1@x.io"]);
  assert(flush !== null, "a flush was scheduled");
  await r.flush();
  assert(calls.length === 2, `chunked at maxBatch: ${JSON.stringify(calls)}`);
  assert(calls.flat().length === 3, "each distinct id (case-insensitive) sent once");
  assert(r.peek("a1@x.io") === "N-a1@x.io" && r.peek("A1@X.IO") === "N-a1@x.io", "cached, case-insensitive");
  assert(r.peek("b2@x.io") === "b2", "an unanswered id falls back to the default");
  assert(notified === 1, "one notification per batch");
  r.request(["a1@x.io", "b2@x.io"]);
  await r.flush();
  assert(calls.length === 2, "resolved ids are not looked up again");
  r.set("a1@x.io", "Alice");
  assert(r.peek("a1@x.io") === "Alice", "optimistic set");
  r.set("a1@x.io", "");
  assert(r.peek("a1@x.io") === "a1", "clearing restores the default");
}

async function testFailureBacksOff(): Promise<void> {
  let now = 0;
  let calls = 0;
  const r = new NameResolver({
    fetchProfiles: async () => {
      calls++;
      throw new Error("404");
    },
    schedule: () => {},
    now: () => now,
    retryMs: 1000,
  });
  r.request(["jack@x.io"]);
  await r.flush();
  assert(r.peek("jack@x.io") === "jack", "a failed lookup still never shows the address");
  r.request(["jack@x.io"]);
  await r.flush();
  assert(calls === 1, "not retried inside the back-off");
  now = 2000;
  r.request(["jack@x.io"]);
  await r.flush();
  assert(calls === 2, "retried after the back-off");
}

function testMentions(): void {
  const agents = [{ ownerId: "maya@x.io", provider: "codex", core: "corex" }];
  const nameOf = (id: string) => (id === "maya@x.io" ? "Maya" : id.split("@")[0]);
  assert(mentionLabel("jill@x.io", agents, nameOf) === "jill", "a person shows the display name");
  assert(mentionLabel("corex/maya@x.io/codex", agents, nameOf) === "Maya · codex (corex)", "a roster agent shows its display label");
  assert(mentionLabel("ann@x.io/claude-code", [], nameOf) === "ann · claude-code", "an unknown handle still never shows the address");
  const candidates = ["maya@x.io", "corex/maya@x.io/codex", "jill@x.io"];
  const labelOf = (m: string) => mentionLabel(m, agents, nameOf);
  assert(mentionMatches(candidates, "Ma", labelOf).length === 2, "matches by display name");
  assert(mentionMatches(candidates, "corex/", labelOf).join() === "corex/maya@x.io/codex", "matches by canonical handle");
  assert(mentionMatches(candidates, "", labelOf, 2).length === 2, "limit");
}

testDefaultRule();
testMentions();
testHandle();
testValidation();
testMasking();
await testBatchingAndDedupe();
await testFailureBacksOff();
