import assert from "node:assert/strict";
import { test } from "node:test";
import { buildIdentityLdflags, goEnvForTriple, requestedTriple, sidecarFileName, sidecarPlan, UNIVERSAL_MAC } from "./build-bridge-sidecar.mjs";

test("maps release triples to Go targets", () => {
  assert.deepEqual(goEnvForTriple("aarch64-apple-darwin"), { goos: "darwin", goarch: "arm64", exeSuffix: "" });
  assert.deepEqual(goEnvForTriple("x86_64-apple-darwin"), { goos: "darwin", goarch: "amd64", exeSuffix: "" });
  assert.deepEqual(goEnvForTriple("x86_64-pc-windows-msvc"), { goos: "windows", goarch: "amd64", exeSuffix: ".exe" });
  assert.deepEqual(goEnvForTriple("x86_64-unknown-linux-gnu"), { goos: "linux", goarch: "amd64", exeSuffix: "" });
  assert.throws(() => goEnvForTriple("riscv64gc-unknown-linux-gnu"), /unsupported target triple/);
  assert.throws(() => goEnvForTriple(UNIVERSAL_MAC), /unsupported target triple/);
});

test("names externalBin files <base>-<triple>[.exe]", () => {
  assert.equal(sidecarFileName("bnbloc-bridge", "x86_64-pc-windows-msvc"), "bnbloc-bridge-x86_64-pc-windows-msvc.exe");
  assert.equal(sidecarFileName("bnbloc-bridge", "x86_64-apple-darwin"), "bnbloc-bridge-x86_64-apple-darwin");
  assert.equal(sidecarFileName("bnbloc-bridge", UNIVERSAL_MAC), "bnbloc-bridge-universal-apple-darwin");
});

test("universal plan builds both arches and lipos them under the universal name", () => {
  const plan = sidecarPlan(UNIVERSAL_MAC, "bnbloc-bridge");
  assert.deepEqual(
    plan.builds.map((b) => [b.triple, b.file, b.goarch]),
    [
      ["aarch64-apple-darwin", "bnbloc-bridge-aarch64-apple-darwin", "arm64"],
      ["x86_64-apple-darwin", "bnbloc-bridge-x86_64-apple-darwin", "amd64"],
    ],
  );
  assert.deepEqual(plan.merge, {
    file: "bnbloc-bridge-universal-apple-darwin",
    parts: ["bnbloc-bridge-aarch64-apple-darwin", "bnbloc-bridge-x86_64-apple-darwin"],
  });
  const single = sidecarPlan("x86_64-pc-windows-msvc", "bnbloc-bridge");
  assert.equal(single.merge, null);
  assert.equal(single.builds[0].file, "bnbloc-bridge-x86_64-pc-windows-msvc.exe");
});

test("target precedence: --target, then TAURI_ENV_TARGET_TRIPLE, then host", () => {
  const host = () => "aarch64-apple-darwin";
  const env = { TAURI_ENV_TARGET_TRIPLE: " universal-apple-darwin " };
  assert.equal(requestedTriple(["--target", "x86_64-apple-darwin"], env, host), "x86_64-apple-darwin");
  assert.equal(requestedTriple(["--target=x86_64-pc-windows-msvc"], env, host), "x86_64-pc-windows-msvc");
  assert.equal(requestedTriple([], env, host), UNIVERSAL_MAC);
  assert.equal(requestedTriple([], {}, host), "aarch64-apple-darwin");
  assert.throws(() => requestedTriple(["--target"], {}, host), /needs a Rust target triple/);
});

test("stamps the build identity into internal/buildinfo", () => {
  assert.equal(
    buildIdentityLdflags("abc123def456", "2026-10-08T00:00:00Z"),
    "-X github.com/luckylhb90/bnbloc/internal/buildinfo.Commit=abc123def456 -X github.com/luckylhb90/bnbloc/internal/buildinfo.BuiltAt=2026-10-08T00:00:00Z",
  );
});
