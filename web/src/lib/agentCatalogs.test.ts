import {
  catalogRefreshKey,
  resolveModelCatalog,
  restrictCatalog,
  type RoomAgentCatalog,
  type RoomModelCatalogs,
} from "./agentCatalogs.ts";
import type { AgentTarget } from "./apiModels.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const mine: AgentTarget = {
  id: "acme/local:codex",
  core: "acme",
  provider: "codex",
  models: ["my-local-a", "my-local-b"],
  defaultModel: "my-local-a",
};

const catalogs: RoomAgentCatalog[] = [
  { ownerUserId: "alice", core: "acme", provider: "codex", online: true, modelOptions: { models: ["my-room"] } },
  {
    ownerUserId: "bob",
    core: "acme",
    provider: "codex",
    online: true,
    modelOptions: {
      models: ["gpt-5", "o3", "o4-mini"],
      defaultModel: "o3",
      reasoningEfforts: ["low", "high"],
      reasoningByModel: { "gpt-5": ["minimal", "high"], o3: ["high"] },
    },
  },
  { ownerUserId: "root", core: "nimbus", provider: "claude-code", online: true, modelOptions: { models: ["opus", "sonnet"] } },
];

const room: RoomModelCatalogs = {
  catalogs,
  grants: [{ ownerUserId: "bob", provider: "acme:codex", allowedModels: ["gpt-5", "o4-mini"] }],
};

function resolve(pick: Parameters<typeof resolveModelCatalog>[0]["pick"], localTargets: AgentTarget[] = [mine], r = room) {
  return resolveModelCatalog({ pick, subject: "alice", localTargets, selectedTargetId: mine.id, room: r });
}

function testNoPickUsesMySelectedLocalTarget(): void {
  assert(resolve(null)?.models?.[0] === "my-local-a", "no recipient: my selected local target");
  assert(resolve(null, []) === null, "no recipient and no local target: no catalog");
}

function testOwnAgentPrefersTheLocalTarget(): void {
  const own = { ownerId: "alice", core: "acme", provider: "codex" };
  assert(resolve(own)?.models?.[0] === "my-local-a", "desktop: the local target is the freshest catalog");
  assert(resolve(own, [])?.models?.[0] === "my-room", "browser: my agent's room catalog");
}

function testAnotherMembersAgentNeverGetsMyCatalog(): void {
  // bob's agent shares my (core, provider): the old owner-less match showed
  // MY catalog here.
  const bobs = resolve({ ownerId: "bob", core: "acme", provider: "codex" });
  assert(!!bobs && !bobs.models?.includes("my-local-a") && !bobs.models?.includes("my-room"), "never my catalog for bob's agent");
  const unknown = resolve({ ownerId: "carol", core: "acme", provider: "codex" });
  assert(unknown === null, "an owner without a room catalog has no picker (no fallback to mine)");
  const otherCore = resolve({ ownerId: "bob", core: "other", provider: "codex" });
  assert(otherCore === null, "the core is part of the identity");
}

function testBorrowedAgentIsNarrowedToTheGrant(): void {
  const bobs = resolve({ ownerId: "bob", core: "acme", provider: "codex" });
  assert(bobs?.models?.join(",") === "gpt-5,o4-mini", "only the authorized models, in catalog order");
  assert(bobs?.defaultModel === undefined, "an unauthorized default is dropped");
  assert(Object.keys(bobs?.reasoningByModel ?? {}).join(",") === "gpt-5", "per-model reasoning follows the narrowed models");
  assert(bobs?.reasoningEfforts?.join(",") === "low,high", "the target-level ladder is kept");
  const unrestricted = resolve({ ownerId: "bob", core: "acme", provider: "codex" }, [mine], {
    catalogs,
    grants: [{ ownerUserId: "bob", provider: "acme:codex", allowedModels: [] }],
  });
  assert(unrestricted?.models?.length === 3 && unrestricted.defaultModel === "o3", "an empty allowedModels is unrestricted");
  const noGrant = resolve({ ownerId: "bob", core: "acme", provider: "codex" }, [mine], { catalogs, grants: [] });
  assert(noGrant?.models?.length === 3, "not yet authorized: the owner's full catalog");
}

function testPlatformAgentUsesTheHostCatalogUnrestricted(): void {
  const platform = resolve({ ownerId: "root", core: "nimbus", provider: "claude-code", platform: true }, [], {
    catalogs,
    grants: [{ ownerUserId: "root", provider: "nimbus:claude-code", allowedModels: ["opus"] }],
  });
  assert(platform?.models?.join(",") === "opus,sonnet", "platform grants carry no model range");
  assert(platform?.core === "nimbus" && platform.provider === "claude-code", "the catalog names its agent");
}

function testPlatformDefaultModelIsThePickersDefault(): void {
  const withDefault = resolve({ ownerId: "root", core: "nimbus", provider: "claude-code", platform: true, defaultModel: "sonnet" }, [], {
    catalogs,
    grants: [],
  });
  assert(withDefault?.defaultModel === "sonnet", "the policy default replaces the catalog default");
  assert(withDefault?.models?.join(",") === "opus,sonnet", "the catalog is unchanged when it lists the default");
  const unlisted = resolve({ ownerId: "nobody", core: "nimbus", provider: "claude-code", platform: true, defaultModel: "x-free" }, [], {
    catalogs,
    grants: [],
  });
  assert(unlisted?.defaultModel === "x-free" && unlisted.models?.join(",") === "x-free", "an unlisted default is still offered");
}

function testRestrictKeepsAnAuthorizedDefault(): void {
  const c = restrictCatalog({ provider: "codex", models: ["a", "b"], defaultModel: "b" }, ["b"]);
  assert(c.models?.join(",") === "b" && c.defaultModel === "b", "an authorized default stays");
}

function testRefreshKeyFollowsTheAgentSet(): void {
  const a = catalogRefreshKey([{ ownerId: "bob", core: "c", provider: "codex" }, { ownerId: "alice", provider: "x" }]);
  const b = catalogRefreshKey([{ ownerId: "alice", provider: "x" }, { ownerId: "bob", core: "c", provider: "codex" }]);
  assert(a === b, "order does not change the key");
  assert(a !== catalogRefreshKey([{ ownerId: "Bob", core: "c", provider: "codex" }, { ownerId: "alice", provider: "x" }]), "another owner (subjects are case-sensitive) changes the key");
  assert(a !== catalogRefreshKey([{ ownerId: "alice", provider: "x" }]), "a removed agent changes the key");
}

testNoPickUsesMySelectedLocalTarget();
testOwnAgentPrefersTheLocalTarget();
testAnotherMembersAgentNeverGetsMyCatalog();
testBorrowedAgentIsNarrowedToTheGrant();
testPlatformAgentUsesTheHostCatalogUnrestricted();
testPlatformDefaultModelIsThePickersDefault();
testRestrictKeepsAnAuthorizedDefault();
testRefreshKeyFollowsTheAgentSet();

// The names an agent gives its models travel with the catalog: a borrower's list is
// narrowed to the authorized models and keeps the names of the ones left.
{
  const named: RoomAgentCatalog[] = [
    { ownerUserId: "alice", core: "acme", provider: "codex", online: true, modelOptions: { models: ["m1", "m2"], modelNames: { m1: "Model One", m2: "Model Two" }, defaultModel: "m1" } },
  ];
  const room: RoomModelCatalogs = { catalogs: named, grants: [{ ownerUserId: "alice", provider: "acme:codex", allowedModels: ["m2"] } as never] };
  const got = resolveModelCatalog({ pick: { ownerId: "alice", core: "acme", provider: "codex" }, subject: "bob", localTargets: [], selectedTargetId: "", room });
  assert(got?.modelNames?.m2 === "Model Two", "the borrower's catalog keeps the names");
  assert(JSON.stringify(got?.models) === JSON.stringify(["m2"]), "and is still narrowed to the grant");
}
