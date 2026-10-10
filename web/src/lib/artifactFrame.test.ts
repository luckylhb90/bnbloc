import {
  ARTIFACT_CSP,
  ARTIFACT_STATE_LIMIT,
  artifactRunnerUrl,
  composeArtifactDocument,
  contentKey,
  loadMessage,
  readGuestMessage,
} from "./artifactFrame.ts";

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
  const big = "x".repeat(ARTIFACT_STATE_LIMIT);
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

testRunnerUrlUsesTheShellSchemeOrTheBundle();
testComposedDocumentAppliesThePolicyBeforeTheContent();
testGuestMessagesMustComeFromTheSandboxedFrame();
testStateIntentsAreBoundedJson();
testLoadMessageAndContentKey();
console.log("artifactFrame tests passed");
