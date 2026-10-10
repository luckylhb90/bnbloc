import assert from "node:assert/strict";
import test from "node:test";
import { isGatewayArtifactUrl, isPhoneBrowser, shouldShowDesktopHandoff } from "../src/lib/desktopHandoff.ts";

test("handoff waits for a successful empty own-agent roster", () => {
  const base = {
    subject: "Alice",
    inDesktopShell: false,
    userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
    roomAgents: [],
    roomRosterKnown: true,
  };
  assert.equal(shouldShowDesktopHandoff({ ...base, ownAgents: undefined }), false);
  assert.equal(shouldShowDesktopHandoff({ ...base, roomRosterKnown: false, ownAgents: [] }), false);
  assert.equal(shouldShowDesktopHandoff({ ...base, ownAgents: [] }), true);
});

test("handoff remains contextual to browser own-agent absence", () => {
  const base = {
    subject: "Alice",
    inDesktopShell: false,
    userAgent: "Mozilla/5.0 (X11; Linux x86_64)",
    ownAgents: [],
    roomAgents: [],
    roomRosterKnown: true,
  };
  assert.equal(shouldShowDesktopHandoff({ ...base, inDesktopShell: true }), false);
  assert.equal(shouldShowDesktopHandoff({ ...base, ownAgents: [{ online: true }] }), false);
  assert.equal(shouldShowDesktopHandoff({ ...base, roomAgents: [{ ownerUserId: "alice" }] }), false);
  assert.equal(shouldShowDesktopHandoff({ ...base, roomAgents: [{ ownerUserId: "bob" }] }), true);
});

test("phone user agents receive chat guidance but not desktop handoff", () => {
  assert.equal(isPhoneBrowser("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)"), true);
  assert.equal(isPhoneBrowser("Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile"), true);
  assert.equal(isPhoneBrowser("Mozilla/5.0 (Linux; Android 14; Pixel Tablet)"), false);
  assert.equal(isPhoneBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)"), false);
});

test("only same-gateway artifact URLs are fetched with the session bearer", () => {
  const gateway = "https://bnbloc.example";
  assert.equal(isGatewayArtifactUrl("https://bnbloc.example/v1/artifacts/sha256/abc", gateway, gateway), true);
  assert.equal(isGatewayArtifactUrl("https://files.example/app.tar.gz", gateway, gateway), false);
  assert.equal(isGatewayArtifactUrl("https://bnbloc.example/other/path", gateway, gateway), false);
  assert.equal(isGatewayArtifactUrl("/v1/artifacts/sha256/abc", "/", gateway), true);
});
