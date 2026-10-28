import assert from "node:assert/strict";
import test from "node:test";
import cases from "../../../services/room/testdata/channel_rules.json" with { type: "json" };
import {
  composerBlock,
  composerDefault,
  directoryMatches,
  mayCreateSection,
  mayDeleteChannel,
  mayManageChannel,
  mayManageSection,
  mayPlaceChannel,
  postRefusalOf,
  railGroups,
  reorderedPositions,
  sharedOrder,
  waitText,
  type DrivableAgent,
} from "./channelRules.ts";
import type { ChannelCreationPolicy, RoomChannel, RoomSection } from "./wire.gen.ts";

// The fixture the room service's rules (services/room) are tested against:
// the web answers every case alike, so a control shows only where the
// server allows the act.
test("the channel rules mirror the room service's, case by case", () => {
  const sec = (owners: readonly string[]) => ({ owners: [...owners] });
  for (const k of cases.manageChannel) {
    const got = mayManageChannel({ role: k.role }, k.user, { ownerId: k.channelOwner }, k.sectionOwners.length > 0 ? sec(k.sectionOwners) : null);
    assert.equal(got, k.want, `manageChannel ${JSON.stringify(k)}`);
  }
  for (const k of cases.placeChannel) {
    const got = mayPlaceChannel({ role: k.role }, k.user, k.policy as ChannelCreationPolicy | "", k.section ? sec(k.section) : null);
    assert.equal(got, k.want, `placeChannel ${JSON.stringify(k)}`);
  }
  for (const k of cases.createSection) {
    assert.equal(mayCreateSection({ role: k.role }, k.policy as ChannelCreationPolicy), k.want, `createSection ${JSON.stringify(k)}`);
  }
  for (const k of cases.manageSection) {
    assert.equal(mayManageSection({ role: k.role }, k.user, sec(k.owners)), k.want, `manageSection ${JSON.stringify(k)}`);
  }
  for (const k of cases.deleteChannel) {
    assert.equal(mayDeleteChannel({ role: k.role }, { isDefault: false }), k.want, `deleteChannel ${JSON.stringify(k)}`);
  }
  assert.equal(mayDeleteChannel({ role: "owner" }, { isDefault: true }), false, "the default channel is never deleted");
  assert.equal(mayManageChannel({ role: "owner", agentHost: true }, "alice", { ownerId: "alice" }, null), false, "an agent host is nobody");
});

const ch = (id: string, over: Partial<RoomChannel> = {}): RoomChannel => ({
  channelId: id, name: id, isDefault: false, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya",
  settings: {}, joined: true, starred: false, hidden: false, ...over,
});
const section = (id: string, position: number, owners: string[] = []): RoomSection => ({
  sectionId: id, name: id, position, visibility: "open", rules: "", owners, joinPolicy: "open", createdBy: "maya", createdAtMs: position,
});

test("the rail shows the joined channels, starred first, grouped by section, the top level first", () => {
  const groups = railGroups(
    [
      ch("general", { isDefault: true }),
      ch("zeta", { position: 0 }),
      ch("alpha", { position: 0, starred: true }),
      ch("beta", { position: 1 }),
      ch("not-joined", { joined: false }),
      ch("hidden", { hidden: true }),
      ch("ops-2", { sectionId: "ops", position: 2 }),
      ch("ops-1", { sectionId: "ops", position: 1 }),
      ch("gone-section", { sectionId: "deleted" }),
      ch("design-a", { sectionId: "design" }),
    ],
    [section("ops", 2), section("design", 1), section("empty", 3)],
  );
  assert.deepEqual(
    groups.map((g) => [g.section?.sectionId ?? "top", g.channels.map((c) => c.channelId)]),
    [
      ["top", ["general", "alpha", "gone-section", "zeta", "beta"]],
      ["design", ["design-a"]],
      ["ops", ["ops-1", "ops-2"]],
    ],
  );
  // The open channel shows even when the member does not follow it; an
  // empty section shows where its owner may add channels.
  const open = railGroups([ch("general", { isDefault: true }), ch("other", { joined: false })], [section("empty", 1, ["maya"])], {
    active: "other",
    keepEmpty: (s) => s.owners.includes("maya"),
  });
  assert.deepEqual(open.map((g) => g.channels.map((c) => c.channelId)), [["general", "other"], []]);
});

test("the directory searches names and purposes, words in any order", () => {
  const list = [ch("releases", { purpose: "Ship notes and dates" }), ch("design", { purpose: "Mockups" }), ch("random")];
  assert.deepEqual(directoryMatches(list, "notes SHIP").map((c) => c.channelId), ["releases"]);
  assert.deepEqual(directoryMatches(list, "  ").length, 3);
  assert.deepEqual(directoryMatches(list, "nothing"), []);
});

test("moving a channel up or down gives the group explicit positions", () => {
  const group = [ch("a", { position: 0 }), ch("b", { position: 0 }), ch("c", { position: 5 })];
  assert.deepEqual(reorderedPositions(group, "c", -1), [{ channelId: "c", position: 1 }, { channelId: "b", position: 2 }]);
  assert.deepEqual(reorderedPositions(group, "a", -1), [], "the first does not move up");
  assert.deepEqual(reorderedPositions(group, "c", 1), [], "the last does not move down");
  assert.deepEqual(reorderedPositions(group, "nope", 1), []);
  // The order moved is everyone's (position, name), never the member's own
  // view: stars, hidden and unjoined channels change nothing, and the
  // default channel is not moved.
  const all = [
    ch("general", { isDefault: true }),
    ch("b", { position: 1, starred: true }),
    ch("a", { position: 1, joined: false }),
    ch("c", { position: 0, hidden: true }),
    ch("other-section", { sectionId: "ops" }),
  ];
  assert.deepEqual(sharedOrder(all, "").map((c) => c.channelId), ["c", "a", "b"]);
  assert.deepEqual(sharedOrder(all, "ops").map((c) => c.channelId), ["other-section"]);
});

test("a refused post is read by its code; the composer says why before a send", () => {
  assert.deepEqual(postRefusalOf({ code: "channel_slow_mode", error: "slow", retryAfterMs: 1500 }), { code: "channel_slow_mode", message: "slow", retryAfterMs: 1500 });
  assert.equal(postRefusalOf({ code: "people_chat", error: "x" }), null, "another code is no channel refusal");
  assert.equal(postRefusalOf(null), null);
  assert.match(composerBlock(ch("x", { archivedAtMs: 5 }), { role: "owner" }) ?? "", /archived/);
  assert.match(composerBlock(ch("x", { settings: { whoPosts: "admins" } }), { role: "member" }) ?? "", /owner and admins/);
  assert.equal(composerBlock(ch("x", { settings: { whoPosts: "admins" } }), { role: "admin" }), null);
  assert.equal(composerBlock(ch("x"), { role: "member" }), null);
  assert.equal(waitText(1200), "2 s");
  assert.equal(waitText(61_000), "2 min");
});

// C8 with two candidates that differ only in what a search would ignore:
// one provider under two cores. The channel's default names one by its key;
// the composer proposes that one, never the first of the provider's.
test("the composer proposes the channel's default agent by its key, else the room's, else asks", () => {
  const acme: DrivableAgent = { ownerId: "jack", core: "acme", provider: "claude" };
  const nimbus: DrivableAgent = { ownerId: "jack", core: "nimbus", provider: "claude" };
  const mine: DrivableAgent = { ownerId: "maya", core: "acme", provider: "codex", self: true };
  const roomDefault = () => mine;
  const drivable = [acme, nimbus, mine];
  assert.deepEqual(composerDefault({ channelDefault: { ownerId: "jack", core: "nimbus", provider: "claude" }, drivable, roomDefault }), { agent: nimbus, from: "channel" });
  // The channel's default the member may not drive (not in their roster): the room's default.
  assert.deepEqual(composerDefault({ channelDefault: { ownerId: "olaf", core: "acme", provider: "claude" }, drivable, roomDefault }), { agent: mine, from: "room" });
  // A default without a core is "no core", never "any core".
  assert.deepEqual(composerDefault({ channelDefault: { ownerId: "jack", provider: "claude" }, drivable, roomDefault }), { agent: mine, from: "room" });
  // Nothing at all: ask (no first, no online).
  assert.deepEqual(composerDefault({ channelDefault: null, drivable, roomDefault: () => null }), { agent: null, from: "ask" });
});
