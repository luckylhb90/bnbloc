import { myFilesRoomId, openLocal, type OpenDeps } from "./localOpen.ts";
import type { LocalOpened } from "./localFiles.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const GW = "https://bnbloc.example.com";

function fakes(versions: Record<string, number> = {}) {
  const calls: string[] = [];
  const binds: unknown[] = [];
  const deps: OpenDeps = {
    gateway: GW,
    ensureRoom: async () => (calls.push("ensureRoom"), "my-files-x"),
    read: async (t) => (calls.push(`read ${t}`), new TextEncoder().encode("bytes").buffer),
    bind: async (_t, b) => void binds.push(b),
    currentVersion: async (_r, a) => (a in versions ? versions[a] : null),
    createFile: async (r, n) => (calls.push(`create ${r} ${n}`), "art_new"),
    publish: async (r, a, _b, _n, base, summary) => (calls.push(`publish ${r} ${a} after v${base}: ${summary}`), base + 1),
  };
  return { deps, calls, binds };
}

const opened = (patch: Partial<LocalOpened> = {}): LocalOpened => ({ token: "tok", name: "deck.pptx", sizeBytes: 5, binding: null, changed: false, ...patch });
const bound = { gateway: GW, roomId: "files", artifactId: "art_1", version: 2 };

async function testANewDocumentLandsInMyFiles(): Promise<void> {
  const f = fakes();
  const at = await openLocal(opened(), f.deps);
  assert(JSON.stringify(at) === JSON.stringify({ roomId: "my-files-x", artifactId: "art_new", version: 1 }), `opened ${JSON.stringify(at)}`);
  assert(f.calls.join("|") === "ensureRoom|read tok|create my-files-x deck.pptx|publish my-files-x art_new after v0: Opened from this computer", f.calls.join("|"));
  assert(JSON.stringify(f.binds) === JSON.stringify([{ gateway: GW, roomId: "my-files-x", artifactId: "art_new", version: 1 }]), "bound to the new file");
}

async function testAReopenGoesBackToItsRoomFile(): Promise<void> {
  // Unchanged: the room file as it is now (an agent may have made v3), no upload.
  let f = fakes({ art_1: 3 });
  let at = await openLocal(opened({ binding: bound }), f.deps);
  assert(at.artifactId === "art_1" && at.version === 3 && f.calls.length === 0, `unchanged ${JSON.stringify(at)} ${f.calls}`);

  // Changed on disk: the next version of that file.
  f = fakes({ art_1: 3 });
  at = await openLocal(opened({ binding: bound, changed: true }), f.deps);
  assert(at.version === 4 && f.calls.includes("publish files art_1 after v3: Changed on this computer"), `changed ${f.calls}`);
  assert(JSON.stringify(f.binds) === JSON.stringify([{ ...bound, version: 4 }]), "rebound at the new version");

  // The room file is gone, or the binding is another gateway's: a new copy.
  f = fakes();
  at = await openLocal(opened({ binding: bound, changed: true }), f.deps);
  assert(at.roomId === "my-files-x" && at.artifactId === "art_new", "a deleted room file starts over in My files");
  f = fakes({ art_1: 3 });
  at = await openLocal(opened({ binding: { ...bound, gateway: "https://other.example.com" } }), f.deps);
  assert(at.artifactId === "art_new", "another gateway's binding is not this one's");
}

async function testMyFilesRoomIdIsStableAndValid(): Promise<void> {
  const a = await myFilesRoomId("acme", "maya");
  assert(a === (await myFilesRoomId("acme", "maya")), "stable");
  assert(a !== (await myFilesRoomId("acme", "jack")) && a !== (await myFilesRoomId("nimbus", "maya")), "per member and organization");
  assert(/^my-files-[0-9a-f]{16}$/.test(a), a);
}

await testANewDocumentLandsInMyFiles();
await testAReopenGoesBackToItsRoomFile();
await testMyFilesRoomIdIsStableAndValid();
