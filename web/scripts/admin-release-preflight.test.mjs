import assert from "node:assert/strict";
import test from "node:test";

import { MANUAL_UPDATE_TARGETS } from "../src/views/updateConfigFields.ts";
import { nextDesktopPublishStep, publishBlockReason, validateDesktopReleaseSelection } from "../src/lib/desktopRelease.ts";


const signature = btoa("untrusted comment: signature from tauri secret key\nabc\ntrusted comment: timestamp:1 file:bnbloc.app.tar.gz\ndef");
const file = new File(["signed updater archive"], "bnbloc.app.tar.gz");
const selected = {
  file,
  signature,
  version: "0.1.3",
  notes: "test notes",
};

test("the wizard advances only after each step's required state is valid", () => {
  const signatureFile = new File([signature], "bnbloc.app.tar.gz.sig");
  assert.equal(nextDesktopPublishStep({ step: 0, archive: null, signature: null, version: "" }), 0);
  assert.equal(nextDesktopPublishStep({ step: 0, archive: file, signature, version: "" }), 1);
  assert.throws(() => nextDesktopPublishStep({ step: 1, archive: file, signature, version: "" }), /valid release version/i);
  assert.equal(nextDesktopPublishStep({ step: 1, archive: file, signature, version: "1.2.3" }), 2);
  assert.equal(nextDesktopPublishStep({ step: 2, archive: file, signature, version: "1.2.3", gatewayReady: false }), 2);
  assert.equal(nextDesktopPublishStep({ step: 2, archive: file, signature, version: "1.2.3", gatewayReady: true }), 3);
  assert.equal(signatureFile.name, "bnbloc.app.tar.gz.sig");
});

test("publish preflight explains invalid semantic versions before upload begins", () => {
  for (const version of ["", "  ", "next", "1.2"]) {
    assert.match(publishBlockReason({ archive: file, signature: "selected", version }), /release version/i);
  }
  assert.equal(publishBlockReason({ archive: file, signature: "selected", version: "1.2.3-rc.1" }), null);
});
test("publish button reports the exact missing selection instead of silently disabling", () => {
  assert.equal(publishBlockReason({ archive: null, signature: null, version: "" }), "Select the signed archive and its matching .sig file.");
  assert.equal(publishBlockReason({ archive: file, signature: null, version: "0.1.3" }), "Select the .sig generated for this exact archive.");
  assert.match(publishBlockReason({ archive: file, signature, version: "" }), /release version/i);
  assert.equal(publishBlockReason({ archive: file, signature, version: "0.1.3" }), null);
});

test("a complete paired Tauri signature is accepted and returned unchanged", () => {
  assert.equal(validateDesktopReleaseSelection(selected), signature);
});

test("DMG, unsigned archive, or malformed signatures are rejected before upload", () => {
  assert.throws(() => validateDesktopReleaseSelection({ ...selected, file: new File(["dmg"], "bnbloc_0.1.3_aarch64.dmg") }), /app\.tar\.gz/i);
  assert.throws(() => validateDesktopReleaseSelection({ ...selected, signature: "" }), /paired.*signature/i);
  assert.throws(() => validateDesktopReleaseSelection({ ...selected, signature: "not-base64" }), /base64/i);
  assert.throws(() => validateDesktopReleaseSelection({ ...selected, signature: btoa("random text") }), /Tauri Minisign/i);
});

test("manual configuration excludes Apple Silicon target already managed by local publish wizard", () => {
  assert.deepEqual(MANUAL_UPDATE_TARGETS, ["darwin-x86_64", "windows-x86_64"]);
});

