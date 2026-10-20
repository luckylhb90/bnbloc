import describeRoom from "../../../internal/contentbuiltin/packages/describe-room/component.json" with { type: "json" };
import { applyDependencies, DEPENDENCY_KINDS, roomChecklist, roomDependencies } from "./roomCapture.ts";
import { partKey, roomSaveFromDraft, toggleRoomPart, type RoomComponentDraft } from "./roomComponents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const draft: RoomComponentDraft = {
  roomId: "r1",
  name: "support-desk",
  displayName: "Support desk",
  description: "",
  welcome: "",
  quickPrompts: [],
  channels: [],
  schedules: [],
  skills: [],
  prompts: [],
  apps: [{ id: "cc_a", name: "lunch-poll", displayName: "Lunch poll", version: "1.0.0", ref: "lunch-poll@^1" }],
  madeApps: [{ artifactId: "art_board", title: "Ticket board", sizeBytes: 1024 }],
  files: [
    { artifactId: "art_faq", title: "faq", fileName: "faq.md", sizeBytes: 2048 },
    { artifactId: "art_log", title: "log", fileName: "log.txt", sizeBytes: 10 },
  ],
  tools: [{ name: "browser", permission: "mcp.stdio" }],
  sources: [],
  left: [],
};

// A reply's dependsOn: an unknown kind is "other", included is a "yes",
// each named once, at most twelve.
{
  const deps = roomDependencies({
    dependsOn: [
      { what: "faq.md", kind: "File", included: "Yes", why: "The answers come from it." },
      { what: "faq.md", kind: "file", included: "no", why: "twice" },
      { what: "The CRM", kind: "account", included: "no", why: "An outside account." },
      { what: " ", kind: "file" },
      "not an item",
    ],
  });
  eq(
    deps,
    [
      { what: "faq.md", kind: "file", included: true, why: "The answers come from it." },
      { what: "The CRM", kind: "other", included: false, why: "An outside account." },
    ],
    "the dependencies read",
  );
  eq(roomDependencies({ dependsOn: Array.from({ length: 15 }, (_, i) => ({ what: `d${i}`, kind: "data", included: "no" })) }).length, 12, "at most twelve");
  eq(roomDependencies({}), [], "none");
}

// The checklist matches a dependency to the part it names (as the input
// wrote it, its size and state beside it), and says what the package
// carries now: the tick box's state, the instructions' text, else the AI's
// word. Fill ticks what the AI says to carry and leaves the rest as the
// member set it.
{
  const f = roomSaveFromDraft(draft);
  const deps = roomDependencies({
    dependsOn: [
      { what: "faq.md (not included, 2.0 KB)", kind: "file", included: "yes", why: "The answers come from it." },
      { what: "Ticket board", kind: "app", included: "yes", why: "Tickets are tracked in it." },
      { what: "Lunch poll", kind: "app", included: "yes", why: "From a work." },
      { what: "browser", kind: "tool", included: "yes", why: "It opens the help center." },
      { what: "log.txt", kind: "file", included: "no", why: "Old logs." },
      { what: "Instructions", kind: "instructions", included: "yes", why: "Replies stay short." },
      { what: "maya/codex", kind: "agent", included: "no", why: "Someone's agent." },
    ],
  });
  const rows = roomChecklist(f, deps);
  eq(
    rows.map((r) => [r.key, r.carried]),
    [
      [partKey("file", "art_faq"), false],
      [partKey("madeApp", "art_board"), true],
      [partKey("app", "lunch-poll@^1"), true],
      [partKey("tool", "browser"), true],
      [partKey("file", "art_log"), false],
      [null, false],
      [null, false],
    ],
    "the rows before Fill",
  );
  const filled = applyDependencies(f, deps);
  eq(roomChecklist(filled, deps)[0].carried, true, "Fill ticks the file the AI says the room needs");
  eq(roomChecklist(filled, deps)[4].carried, false, "and leaves the one it says to leave out");
  eq(roomChecklist({ ...filled, instructions: "Keep it short." }, deps)[5].carried, true, "instructions carried once written");
  const unticked = toggleRoomPart(filled, partKey("madeApp", "art_board"), false);
  eq(roomChecklist(unticked, deps)[1].carried, false, "a row follows the member's tick");
  eq(applyDependencies(f, []), f, "no dependencies, no change");
}

// The kinds are describe-room's, in its order.
{
  const dep = describeRoom.fields.find((f) => f.name === "dependsOn") as { item: { name: string; description: string }[] } | undefined;
  eq(dep?.item.map((i) => i.name), ["what", "kind", "included", "why"], "a dependency's fields are the package's");
  const kinds = dep?.item.find((i) => i.name === "kind")?.description ?? "";
  eq(kinds, `${DEPENDENCY_KINDS.slice(0, -1).join(", ")} or ${DEPENDENCY_KINDS[DEPENDENCY_KINDS.length - 1]}`, "the kinds are the package's");
}
