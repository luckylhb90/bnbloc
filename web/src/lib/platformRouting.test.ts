import { BUSIER_BY, pickHost, readLastHost, rememberHost } from "./platformRouting.ts";
import type { PlatformHost } from "./platformAgents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const h = (hostUserId: string, online: boolean, queued = 0): PlatformHost => ({ hostUserId, online, queued, targetId: "" });

function memoryStorage(): Pick<Storage, "getItem" | "setItem"> & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

function testNoOnlineHost(): void {
  assert(pickHost([], null) === null, "no hosts");
  assert(pickHost([h("a", false), h("b", false)], "a") === null, "every host offline: refuse");
}

function testFewestQueued(): void {
  assert(pickHost([h("a", true, 2), h("b", true, 1), h("c", false, 0)], null)?.hostUserId === "b", "least busy online host");
  assert(pickHost([h("a", true, 1), h("b", true, 1)], null)?.hostUserId === "a", "ties keep the listed order");
}

function testStickyLastHost(): void {
  const hosts = [h("a", true, 0), h("b", true, BUSIER_BY - 1)];
  assert(pickHost(hosts, "B")?.hostUserId === "b", "the last host stays while fewer than 3 jobs busier");
  assert(pickHost([h("a", true, 0), h("b", true, BUSIER_BY)], "b")?.hostUserId === "a", "3+ busier: move to the least busy");
  assert(pickHost([h("a", true, 5), h("b", false, 0)], "b")?.hostUserId === "a", "an offline last host is skipped");
}

function testLastHostStorage(): void {
  const s = memoryStorage();
  const codex = { core: "nimbus", provider: "codex" };
  assert(readLastHost(s, "jack", "r1", codex) === null, "nothing remembered");
  rememberHost(s, "Jack", "r1", codex, "rose");
  assert(readLastHost(s, "jack", "r1", { core: "Nimbus", provider: "Codex" }) === "rose", "per member, room and agent");
  assert(readLastHost(s, "jack", "r2", codex) === null, "another room has its own host");
  assert(readLastHost(s, "bob", "r1", codex) === null, "another member has their own host");
  assert(readLastHost(null, "jack", "r1", codex) === null, "no storage");
  s.data.set("bnbloc.platformHosts", "not json");
  assert(readLastHost(s, "jack", "r1", codex) === null, "a corrupt entry reads as none");
  for (let i = 0; i < 250; i++) rememberHost(s, "jack", `room${i}`, codex, "rose");
  const kept = Object.keys(JSON.parse(s.data.get("bnbloc.platformHosts") ?? "{}"));
  assert(kept.length === 200 && kept.at(-1)?.includes("room249") === true, "bounded, newest kept");
}

testNoOnlineHost();
testFewestQueued();
testStickyLastHost();
testLastHostStorage();
