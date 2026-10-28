import { emptyLayout, layoutFromPlan, layoutGroups, layoutSize, makeLayout, type LayoutApi, type RoomLayout } from "./roomLayout.ts";
import { MaxChannelPurpose } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const layout: RoomLayout = {
  sections: [{ name: "Product", rules: "Decisions go to #decisions." }, { name: "Engineering" }],
  channels: [
    { name: "announcements", purpose: "Notices", settings: { whoPosts: "admins" } },
    { name: "requests", section: "product", settings: {} },
    { name: "decisions", section: "Product", rules: "One a post.", settings: {} },
    { name: "alerts", section: "Engineering", settings: { slowModeSeconds: 30 } },
    { name: "stray", section: "Gone", settings: {} },
  ],
};

// The preview is the rail: #general and the top level first, then each
// section with its channels; a channel in a section not made is top level.
{
  const groups = layoutGroups(layout);
  eq(
    groups.map((g) => [g.section?.name ?? "top", g.channels.map((c) => c.name)]),
    [
      ["top", ["general", "announcements", "stray"]],
      ["Product", ["requests", "decisions"]],
      ["Engineering", ["alerts"]],
    ],
    "the preview's groups",
  );
  eq(layoutGroups(emptyLayout).map((g) => g.channels.map((c) => c.name)), [["general"]], "an empty layout is #general alone");
  eq(layoutSize(layout), "2 sections · 5 channels", "the size line");
  eq(layoutSize({ sections: [], channels: [{ name: "a", settings: {} }] }), "1 channel", "one channel, no sections");
}

// A plan's layout is held to a package's rules: names once, bounded, the
// default channel never, purposes cut, a section only when made.
{
  const long = "p".repeat(MaxChannelPurpose + 10);
  const got = layoutFromPlan(
    [{ name: " Work " }, { name: "work" }, { name: "" }],
    [{ name: "General" }, { name: "a", purpose: long, section: "WORK" }, { name: "A" }, { name: "b\nc" }],
  );
  eq(got.sections, [{ name: "Work" }], "sections named once, trimmed");
  eq(
    got.channels.map((c) => [c.name, c.purpose?.length ?? 0, c.section ?? ""]),
    [
      ["a", MaxChannelPurpose, "Work"],
      ["b c", 0, ""],
    ],
    "channels: no #general, no repeat, one line, the purpose cut",
  );
  eq(layoutFromPlan([], Array.from({ length: 30 }, (_, i) => ({ name: `c${i}` }))).channels.length, 20, "at most 20 channels");
}

// Making a layout: sections first, then each channel in its section, then
// its position, rules and settings; a refusal names the part.
{
  const calls: string[] = [];
  const api: LayoutApi = {
    createSection: async (_room, body) => {
      calls.push(`section ${body.name} ${body.position} ${body.visibility}`);
      return { sectionId: `sec_${body.name}` } as never;
    },
    create: async (_room, body) => {
      calls.push(`channel ${body.name} in ${body.sectionId || "top"}`);
      return { channelId: `ch_${body.name}` };
    },
    patch: async (_room, id, body) => {
      calls.push(`patch ${id} ${JSON.stringify(body)}`);
      return {};
    },
  };
  await makeLayout(api, "r1", { sections: [{ name: "Product" }], channels: [{ name: "a", settings: { whoPosts: "admins" } }, { name: "b", section: "product", rules: "R", settings: {} }] });
  eq(
    calls,
    [
      "section Product 0 open",
      "channel a in top",
      'patch ch_a {"position":0,"settings":{"whoPosts":"admins"}}',
      "channel b in sec_Product",
      'patch ch_b {"position":1,"rules":"R"}',
    ],
    "the calls, in order",
  );
  let said = "";
  await makeLayout({ ...api, create: async () => Promise.reject(new Error("no")) }, "r1", { sections: [], channels: [{ name: "x", settings: {} }] }).catch((e: Error) => {
    said = e.message;
  });
  eq(said, "Could not make #x: no", "a refusal names the channel");
}
