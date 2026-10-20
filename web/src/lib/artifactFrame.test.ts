import {
  ARTIFACT_CSP,
  artifactRunnerUrl,
  composeArtifactDocument,
  contentKey,
  loadMessage,
  MAX_OPEN_FILE_NAME,
  openFileName,
  pickFileByName,
  readGuestMessage,
} from "./artifactFrame.ts";
import { MaxArtifactStateBytes } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testRunnerUrlUsesTheShellSchemeOrTheBundle(): void {
  const calls: string[] = [];
  const desktop = artifactRunnerUrl({
    convertFileSrc: (path, protocol) => {
      calls.push(`${protocol}:${path}`);
      return `${protocol}://localhost/${encodeURIComponent(path)}`;
    },
    baseURI: "tauri://localhost/index.html",
    hostOrigin: "tauri://localhost",
  });
  assert(calls.join() === "bnbloc-artifact:", "desktop takes the shell scheme root from convertFileSrc");
  assert(
    desktop === "bnbloc-artifact://localhost/artifact-runner.html#host=tauri%3A%2F%2Flocalhost",
    `desktop runner url carries the encoded host origin: ${desktop}`,
  );

  const browser = artifactRunnerUrl({
    baseURI: "https://gw.example/#/rooms/r1",
    hostOrigin: "https://gw.example",
  });
  assert(
    browser === "https://gw.example/artifact-runner.html#host=https%3A%2F%2Fgw.example",
    `browser runner sits beside index.html: ${browser}`,
  );
}

function testComposedDocumentAppliesThePolicyBeforeTheContent(): void {
  const doc = composeArtifactDocument("<script>window.x = 1</script>");
  const csp = doc.indexOf(`content="${ARTIFACT_CSP}"`);
  const boot = doc.indexOf('type: "artifact-ready"');
  const content = doc.indexOf("window.x = 1");
  assert(csp > 0 && boot > csp && content > boot, "policy, then bootstrap, then artifact content");
  assert(ARTIFACT_CSP.startsWith("default-src 'none';"), "artifact refuses every external load by default");
  assert(!/connect-src|https?:/.test(ARTIFACT_CSP), "no network origin is ever allowed to the artifact");
}

function testGuestMessagesMustComeFromTheSandboxedFrame(): void {
  const frame = {};
  const other = {};
  const ready = { type: "artifact-ready" };
  assert(readGuestMessage({ origin: "null", source: frame, data: ready }, frame)?.kind === "ready", "accepts its own frame");
  assert(readGuestMessage({ origin: "null", source: other, data: ready }, frame) === null, "rejects another window");
  assert(
    readGuestMessage({ origin: "https://gw.example", source: frame, data: ready }, frame) === null,
    "a non-opaque origin means the frame is not sandboxed: rejected",
  );
  assert(readGuestMessage({ origin: "null", source: null, data: ready }, null) === null, "no frame, no messages");
  assert(
    readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-runner-ready" } }, frame)?.kind ===
      "runner-ready",
    "runner hand-off request",
  );
  for (const data of [null, "artifact-ready", 7, { type: "artifact-load", html: "x" }, { type: "other" }]) {
    assert(readGuestMessage({ origin: "null", source: frame, data }, frame) === null, `ignores ${JSON.stringify(data)}`);
  }
}

function testStateIntentsAreBoundedJson(): void {
  const frame = {};
  const ok = readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-state", state: { a: 1 } } }, frame);
  assert(ok?.kind === "state" && ok.key === '{"a":1}', "state carries its serialized dedupe key");
  const big = "\u00e9".repeat(MaxArtifactStateBytes / 2);
  assert(
    readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-state", state: big } }, frame) === null,
    "oversized state is dropped",
  );
  const cyclic: Record<string, unknown> = {};
  cyclic.self = cyclic;
  assert(
    readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-state", state: cyclic } }, frame) === null,
    "non-serializable state is dropped",
  );
  assert(
    readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-state" } }, frame) === null,
    "missing state (undefined, not JSON) is dropped",
  );
}

function testLoadMessageAndContentKey(): void {
  const msg = loadMessage("<p>hi</p>");
  assert(msg.type === "artifact-load" && msg.html === "<p>hi</p>", "hand-off shape the runner expects");
  assert(contentKey("abc") === contentKey("abc"), "content key is deterministic");
  assert(contentKey("abc") !== contentKey("abd"), "content change changes the key");
}

// A relative link becomes a request to open a room file by its base name;
// the host takes only a plain name from the sandboxed frame.
function testOpenFileRequests(): void {
  const frame = {};
  const msg = (name: unknown) => readGuestMessage({ origin: "null", source: frame, data: { type: "artifact-open-file", name } }, frame);
  const ok = msg("run-1.gif");
  assert(ok?.kind === "open-file" && ok.name === "run-1.gif", "a plain name is an open-file request");
  for (const bad of ["", "  ", ".", "..", "a/b.gif", "a\\b.gif", "x\u0000.gif", 7, null, "x".repeat(MAX_OPEN_FILE_NAME + 1)]) {
    assert(msg(bad) === null, `refused name ${JSON.stringify(bad)}`);
  }
  assert(openFileName(" guide.gif ") === "guide.gif", "a name is trimmed");
  assert(readGuestMessage({ origin: "https://evil.example", source: frame, data: { type: "artifact-open-file", name: "a.gif" } }, frame) === null, "only the sandboxed frame asks");
  const doc = composeArtifactDocument("<a href=\"browser-runs/run-1.gif\">GIF</a>");
  assert(doc.includes("artifact-open-file") && doc.includes('closest("a[href]")'), "the bootstrap turns relative links into requests");
  assert(doc.indexOf("artifact-open-file") < doc.indexOf("browser-runs/run-1.gif"), "the bootstrap runs before the content");
}

// Names repeat across runs and channels: the newest file of the name wins,
// matched case-insensitively, by file name or else title.
function testPickFileByName(): void {
  const files = [
    { artifactId: "a", fileName: "run.gif", title: "Run", updatedAtMs: 1 },
    { artifactId: "b", fileName: "RUN.gif", title: "Run", updatedAtMs: 3 },
    { artifactId: "c", fileName: "other.gif", title: "Other", updatedAtMs: 9 },
    { artifactId: "d", title: "notes.md", updatedAtMs: 2 },
  ];
  assert(pickFileByName(files, "run.gif")?.artifactId === "b", "the newest of the name");
  assert(pickFileByName(files, "notes.md")?.artifactId === "d", "a title stands in for a missing file name");
  assert(pickFileByName(files, "missing.gif") === null, "no file of the name");
}

testRunnerUrlUsesTheShellSchemeOrTheBundle();
testComposedDocumentAppliesThePolicyBeforeTheContent();
testGuestMessagesMustComeFromTheSandboxedFrame();
testStateIntentsAreBoundedJson();
testLoadMessageAndContentKey();
console.log("artifactFrame tests passed");
testOpenFileRequests();
testPickFileByName();
