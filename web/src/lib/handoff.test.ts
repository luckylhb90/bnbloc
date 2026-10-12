import {
  downloadFilename,
  handoffBannerText,
  importedEditedAtMs,
  importedFrom,
  importSummary,
  pendingSuggestions,
  suggestedInviteRole,
  type RoomHandoffInfo,
} from "./handoff.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
const assert = {
  equal(actual: unknown, expected: unknown, message = ""): void {
    if (actual !== expected) throw new Error(`${message} expected ${String(expected)}, got ${String(actual)}`);
  },
  deepEqual(actual: unknown, expected: unknown): void {
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
    }
  },
};

test("importedFrom reads the server marker, name first", () => {
  assert.equal(importedFrom({ text: "hi" }), undefined);
  assert.equal(importedFrom(undefined), undefined);
  assert.equal(importedFrom({ imported: "yes" }), undefined);
  assert.equal(importedFrom({ imported: { fromRoomId: "src", fromRoomName: "Launch" } }), "Launch");
  assert.equal(importedFrom({ imported: { fromRoomId: "src", fromRoomName: " " } }), "src");
  assert.equal(importedEditedAtMs({ imported: { fromRoomId: "src", editedAtMs: 5 } }), 5);
  assert.equal(importedEditedAtMs({ imported: { fromRoomId: "src" } }), undefined);
});

const info: RoomHandoffInfo = {
  roomId: "launch-1a2b3c4d",
  sourceRoomId: "launch",
  sourceRoomName: "Launch plan",
  sourceExportedAtMs: 0,
  sourceExportedBy: "alice",
  importedBy: "dave",
  importedAtMs: Date.UTC(2026, 9, 8, 12),
  suggestedMembers: [
    { userId: "alice", role: "owner" },
    { userId: "Bob", role: "member" },
    { userId: "vic", role: "visitor" },
  ],
};

test("banner names the source room and the import date", () => {
  const text = handoffBannerText(info, "en-US");
  assert.equal(text.startsWith("Imported from Launch plan on "), true, text);
  assert.equal(text.includes("2026"), true, text);
  assert.equal(handoffBannerText({ ...info, sourceRoomName: "" }, "en-US").startsWith("Imported from launch on"), true);
});

test("suggestions never hand out ownership and skip current members", () => {
  assert.deepEqual(info.suggestedMembers.map((m) => suggestedInviteRole(m.role)), ["admin", "member", "visitor"]);
  assert.deepEqual(pendingSuggestions(info.suggestedMembers, ["dave", "Bob"]).map((m) => m.userId), ["alice", "vic"]);
  // Subjects are case-sensitive: "bob" is not the suggested "Bob".
  assert.deepEqual(pendingSuggestions(info.suggestedMembers, ["dave", "bob"]).map((m) => m.userId), ["alice", "Bob", "vic"]);
});

test("download filename comes from Content-Disposition", () => {
  assert.equal(downloadFilename('attachment; filename="launch-handoff-20261008.zip"', "launch"), "launch-handoff-20261008.zip");
  assert.equal(downloadFilename("attachment; filename*=UTF-8''r%C3%A4um.zip", "x"), "räum.zip");
  assert.equal(downloadFilename(null, "a b/c"), "a_b_c-handoff.zip");
});

test("import summary reports counts and skipped attachments", () => {
  const base = { roomId: "r", name: "Launch plan", importedArtifacts: 0, importedAttachments: 0, skippedAttachments: [], handoff: info };
  assert.equal(importSummary({ ...base, importedMessages: 1 }), 'Imported 1 message into "Launch plan"');
  assert.equal(
    importSummary({ ...base, importedMessages: 3, importedArtifacts: 2, importedAttachments: 1, skippedAttachments: ["att_1"] }),
    'Imported 3 messages, 2 artifacts, 1 file into "Launch plan" (1 attachment skipped)',
  );
});
