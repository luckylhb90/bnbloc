import {
  addFailureMessage,
  composeRoomAgents,
  consoleAgentRows,
  defaultModelOptions,
  defaultPlatformRecipient,
  hostsLabel,
  isPlatformOffline,
  jobQueueAhead,
  myHostPolicy,
  otherHostPolicies,
  platformChoices,
  PLATFORM_AGENTS_GROUP,
  routeSend,
  samePolicyInput,
  setPolicyDefaultModel,
  splitAgentHosts,
  toggleDraftAgent,
  togglePolicyAgent,
  type PlatformAgent,
  type PlatformHost,
  type RoomPlatformAgent,
} from "./platformAgents.ts";
import { agentRows } from "../views/composer/agentRows.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const host = (hostUserId: string, online: boolean, queued = 0, defaultModel?: string): PlatformHost => ({
  hostUserId,
  online,
  queued,
  targetId: "nimbus/local:x",
  ...(defaultModel ? { defaultModel } : {}),
});
const codex: PlatformAgent = { core: "nimbus", provider: "codex", online: true, hosts: [host("admin", true, 0, "x-free")] };
const claude: PlatformAgent = { core: "nimbus", provider: "claude-code", online: false, hosts: [host("admin", false)] };
const codexInRoom: RoomPlatformAgent = { ...codex, hosts: [host("admin", false)], addedBy: "jack", addedAtMs: 1 };

function testAgentHostsAreNeverPeople(): void {
  const { people, agentHosts } = splitAgentHosts([
    { userId: "jack", role: "owner", agentHost: false },
    { userId: "admin", role: "member", agentHost: true },
    { userId: "rose", role: "member", agentHost: false },
  ]);
  assert(people.map((m) => m.userId).join(",") === "jack,rose", "people exclude the agent host");
  assert(agentHosts.join(",") === "admin", "the agent host is reported separately");
}

function testComposeRoomAgents(): void {
  const agents = composeRoomAgents({
    roster: [
      { ownerUserId: "jack", core: "nimbus", provider: "codex" },
      // The host's bridge advertises every agent of theirs; only the added
      // platform agent may surface.
      { ownerUserId: "admin", core: "nimbus", provider: "codex" },
      { ownerUserId: "admin", core: "nimbus", provider: "secret" },
    ],
    // The platform grant is listed by the authority like any borrow grant.
    shared: [{ ownerUserId: "admin", provider: "nimbus:codex" }],
    platform: [codexInRoom],
    agentHosts: ["admin"],
    subject: "jack",
  });
  const handles = agents.map((a) => `${a.ownerId}/${a.core ?? ""}/${a.provider}${a.platform ? "*" : ""}`);
  assert(handles.join(",") === "jack/nimbus/codex,admin/nimbus/codex*", `got ${handles.join(",")}`);
  const platform = agents.find((a) => a.platform);
  assert(!!platform && !platform.self && platform.sandboxed === true, "a platform agent is borrowed and sandboxed");
}

function testHostWhoIsAlsoAPersonKeepsTheirOwnAgents(): void {
  const agents = composeRoomAgents({
    roster: [{ ownerUserId: "admin", core: "nimbus", provider: "secret" }],
    shared: [],
    platform: [codexInRoom],
    agentHosts: [],
    subject: "jack",
  });
  assert(agents.some((a) => a.provider === "secret" && !a.platform), "a human member's own agents stay borrowable");
}

function testPlatformChoices(): void {
  const choices = platformChoices([codex, claude], [codexInRoom], "jack");
  assert(choices.length === 2, "one choice per hosted agent");
  const c = choices.find((x) => x.agent.provider === "codex");
  assert(!!c && c.inRoom && c.online, "in-room flag from the room list, online from the live offer");
  assert(c?.agent.defaultModel === "x-free", "the routed host's default model rides on the recipient");
  assert(choices.find((x) => x.agent.provider === "claude-code")?.inRoom === false, "not yet added");
  assert(platformChoices([codex], [], "admin").length === 0, "the host is not offered their own agents");
  assert(platformChoices([codex], [], "Admin").length === 1, "Admin is another member");
}

function testMultiHostAgentIsOneChoice(): void {
  const both: PlatformAgent = { core: "nimbus", provider: "codex", online: true, hosts: [host("admin", true, 2), host("rose", true, 0)] };
  const choices = platformChoices([both], [], "jack");
  assert(choices.length === 1, "one row per (core, provider)");
  assert(choices[0].agent.ownerId === "rose", "routed to the least busy online host");
  assert(choices[0].hosts.length === 2, "every host is listed");
  const sticky = platformChoices([both], [], "jack", () => "admin");
  assert(sticky[0].agent.ownerId === "admin", "the room's last host while not 3+ busier");
  const asHost = platformChoices([both], [], "rose");
  assert(asHost.length === 1 && asHost[0].hosts.map((h) => h.hostUserId).join(",") === "admin", "a host is served by the others");
  const offline = platformChoices([{ ...both, hosts: [host("admin", false)] }], [], "jack");
  assert(offline[0].online === false, "no online host: the choice is offline");
  assert(hostsLabel(both.hosts) === "admin (2 queued), rose", `got ${hostsLabel(both.hosts)}`);
  const mailHosts = [host("admin@corp.example.com", true), host("rose@corp.example.com", false)];
  assert(hostsLabel(mailHosts) === "admin, rose (offline)", "hosts render by the default display name, never the address");
  assert(hostsLabel(mailHosts, (id) => (id.startsWith("admin") ? "Ada" : "Rose")) === "Ada, Rose (offline)", "resolved names are used");
}

function testRoomHostsAreTheRoomListing(): void {
  const offer: PlatformAgent = { core: "nimbus", provider: "codex", online: true, hosts: [host("admin", true, 5, "x-free"), host("rose", true, 0)] };
  const added: RoomPlatformAgent = { core: "nimbus", provider: "codex", online: false, hosts: [host("admin", false)], addedBy: "", addedAtMs: 0 };
  const [c] = platformChoices([offer], [added], "jack");
  assert(c.hosts.length === 1 && c.agent.ownerId === "admin", "only the hosts serving the room");
  assert(c.online && c.hosts[0].queued === 5 && c.agent.defaultModel === "x-free", "with the live offer's state");
}

function testComposeRoutesRoomPlatformAgents(): void {
  const inRoom: RoomPlatformAgent = {
    core: "nimbus",
    provider: "codex",
    online: true,
    hosts: [host("admin", true, 1), host("rose", true, 0)],
    addedBy: "jack",
    addedAtMs: 1,
  };
  const agents = composeRoomAgents({
    roster: [{ ownerUserId: "rose", core: "nimbus", provider: "codex" }],
    shared: [
      { ownerUserId: "admin", provider: "nimbus:codex" },
      { ownerUserId: "rose", provider: "nimbus:codex" },
    ],
    platform: [inRoom],
    agentHosts: ["admin", "rose"],
    subject: "jack",
    lastHost: () => "admin",
  });
  assert(agents.length === 1 && agents[0].platform === true, "every host's grant folds into the one platform agent");
  assert(agents[0].ownerId === "admin", "the remembered host serves while healthy");
}

function testQueueAheadAndOfflineRefusal(): void {
  const inRoom: RoomPlatformAgent = { core: "nimbus", provider: "codex", online: true, hosts: [host("admin", true, 3)], addedBy: "", addedAtMs: 0 };
  const job = { status: "queued" as const, ownerUserId: "admin", core: "nimbus", provider: "codex" };
  assert(jobQueueAhead(job, [inRoom]) === 2, "the host's queue counts the job itself");
  assert(jobQueueAhead({ ...job, status: "running" }, [inRoom]) === 0, "a started job is not waiting");
  assert(jobQueueAhead({ ...job, failureReason: "no_bridge_online" }, [inRoom]) === 0, "a retry keeps its own label");
  assert(jobQueueAhead({ ...job, ownerUserId: "bob" }, [inRoom]) === 0, "not a platform host");
  assert(isPlatformOffline(409, "platform_agent_offline"), "the chat refusal is recognized");
  assert(!isPlatformOffline(409, "conflict"), "another conflict is not");
}

function testPickerGroupsPlatformAgentsOnce(): void {
  const choices = platformChoices([codex, claude], [codexInRoom], "jack");
  const roomAgents = composeRoomAgents({ roster: [], shared: [], platform: [codexInRoom], agentHosts: ["admin"], subject: "jack" });
  const groups = agentRows([{ id: "nimbus/local:codex", core: "nimbus", provider: "codex" }], roomAgents, "jack", choices);
  assert(groups.at(-1)?.group === PLATFORM_AGENTS_GROUP, "platform agents are the last group");
  const all = groups.flatMap((g) => g.rows);
  assert(all.filter((r) => r.ownerId === "admin" && r.provider === "codex").length === 1, "the added platform agent is one row");
  assert(all.find((r) => r.ownerId === "jack")?.localTargetId === "nimbus/local:codex", "my own codex stays mine");
}

function testAddFailureMessages(): void {
  assert(addFailureMessage(403, "visitor", "codex") === "Only room members who can chat may add agents to this room", "visitor");
  assert(addFailureMessage(404, "not published", "codex") === "codex is no longer offered as a platform agent", "unpublished");
}

function testConsoleRows(): void {
  const mine = [
    { core: "nimbus", provider: "codex", online: true, models: ["x-free", "gpt-5"] },
    { core: "nimbus", provider: "claude-code", online: true, models: [] },
  ];
  const policy = {
    hosts: [
      { hostUserId: "admin", enabled: true, agents: [{ core: "nimbus", provider: "codex", defaultModel: "x-free" }, { core: "orbit", provider: "gemini" }] },
      { hostUserId: "rose", enabled: false, agents: [{ core: "nimbus", provider: "codex" }] },
    ],
  };
  const entry = myHostPolicy(policy, "admin");
  assert(entry?.agents.length === 2, "the caller's own entry");
  assert(otherHostPolicies(policy, "admin").map((h) => h.hostUserId).join(",") === "rose", "other hosts are read-only");
  const rows = consoleAgentRows(mine, entry?.agents ?? []);
  assert(rows.length === 3, "own agents plus a published agent the bridge does not advertise now");
  assert(rows.filter((r) => r.published).length === 2, "published rows are checked");
  assert(rows.find((r) => r.provider === "gemini")?.mine === false, "flagged as not advertised");
  assert(rows.find((r) => r.provider === "codex")?.models.join(",") === "x-free,gpt-5", "the host's own catalog");
  assert(consoleAgentRows(mine, myHostPolicy(policy, "nobody")?.agents ?? []).every((r) => !r.published), "no entry yet");
  const next = togglePolicyAgent(togglePolicyAgent([], { core: "a", provider: "b" }, true), { core: "A", provider: "B" }, false);
  assert(next.length === 0, "toggle matches case-insensitively");
  const withDefault = setPolicyDefaultModel([{ core: "a", provider: "b" }], { core: "a", provider: "b" }, "x-free");
  assert(withDefault[0].defaultModel === "x-free", "a default model is set");
  assert(setPolicyDefaultModel(withDefault, { core: "a", provider: "b" }, "")[0].defaultModel === undefined, "cleared");
  assert(
    samePolicyInput({ enabled: true, agents: [{ core: "a", provider: "b" }, { core: "c", provider: "d" }] }, { enabled: true, agents: [{ core: "c", provider: "d" }, { core: "A", provider: "B" }] }),
    "order-insensitive",
  );
  assert(!samePolicyInput({ enabled: true, agents: withDefault }, { enabled: true, agents: [{ core: "a", provider: "b" }] }), "a default change is a change");
  assert(defaultModelOptions(["a"], "gone").join(",") === "a,gone", "a saved default the catalog dropped stays selectable");
}

testAgentHostsAreNeverPeople();
testComposeRoomAgents();
testHostWhoIsAlsoAPersonKeepsTheirOwnAgents();
testPlatformChoices();
testMultiHostAgentIsOneChoice();
testRoomHostsAreTheRoomListing();
testComposeRoutesRoomPlatformAgents();
testQueueAheadAndOfflineRefusal();
testPickerGroupsPlatformAgentsOnce();
testAddFailureMessages();
testConsoleRows();

{
  const own = { ownerId: "me", provider: "codex", core: "nimbus", self: true };
  const plat = { ownerId: "admin", provider: "claude-code", core: "nimbus", self: false, platform: true };
  const borrowed = { ownerId: "jack", provider: "codex", core: "nimbus", self: false };
  if (defaultPlatformRecipient([borrowed, plat], false) !== plat) throw new Error("a member without agents lands on the platform agent");
  if (defaultPlatformRecipient([borrowed, plat], true) !== null) throw new Error("a member with an own agent keeps their default");
  if (defaultPlatformRecipient([own, borrowed], false) !== null) throw new Error("no platform agent, no fallback");
}

{
  const codex = { core: "nimbus", provider: "codex" };
  const claude = { core: "nimbus", provider: "claude-code" };
  const off = { enabled: false, agents: [] };
  const first = toggleDraftAgent(off, codex, true);
  if (!first.enabled || first.agents.length !== 1) throw new Error("ticking the first agent of an empty, off entry switches it on");
  const kept = toggleDraftAgent({ enabled: false, agents: [{ ...codex }] }, claude, true);
  if (kept.enabled) throw new Error("an entry deliberately switched off with agents stays off");
  const untick = toggleDraftAgent({ enabled: true, agents: [{ ...codex }] }, codex, false);
  if (!untick.enabled || untick.agents.length !== 0) throw new Error("unticking never changes the switch");
}

// routeSend: the composer's send and its team plan resolve a platform
// recipient to the same routed host; other recipients go as picked.
function testRouteSendSharesTheHost(): void {
  const picked = { ownerId: "admin", core: "nimbus", provider: "codex", self: false, platform: true, sandboxed: true };
  const route = () => host("host-b", true, 0, "x-free");
  const r = routeSend(picked, route, "maya");
  assert(r.recipient?.ownerId === "host-b" && r.host?.hostUserId === "host-b" && !r.offline, "routed to the picked host");
  assert(r.recipient?.defaultModel === "x-free" && r.recipient.self === false, "the host's default model; not self");
  assert(routeSend(picked, route, "host-b").recipient?.self === true, "self when the caller is the host");
  assert(routeSend(picked, route, "HOST-B").recipient?.self === false, "HOST-B is another member");
  const off = routeSend(picked, () => null, "maya");
  assert(off.offline && off.host === null, "every host offline");
  const own = { ownerId: "maya", provider: "codex", self: true };
  const plain = routeSend(own, route, "maya");
  assert(plain.recipient === own && !plain.offline && plain.host === null, "a non-platform recipient goes as picked");
  assert(routeSend(null, route, "maya").recipient === null, "no recipient");
}
testRouteSendSharesTheHost();
