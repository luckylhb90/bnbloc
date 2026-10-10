#!/usr/bin/env node
// Builds the Go `bnbloc-bridge` binary as a Tauri sidecar (externalBin).
//
// Tauri resolves an external binary by appending the Rust *target triple* to
// the configured base name (binaries/bnbloc-bridge), so the compiled file must
// be named `bnbloc-bridge-<triple>` (with `.exe` on Windows). The triple is, in
// order: `--target <triple>` on the command line, TAURI_ENV_TARGET_TRIPLE (set
// by `cargo tauri build --target <triple>` when it runs beforeBuildCommand),
// else the host triple from `rustc -vV`. It maps to GOOS/GOARCH and
// cross-compiles the same `services/bridge/cmd` used by the standalone daemon
// (CGO off, so no C toolchain is needed for any target).
//
// macOS universal (`--target universal-apple-darwin`): tauri-build compiles the
// app once per arch and each pass resolves `bnbloc-bridge-<arch triple>`; the
// bundler then copies `bnbloc-bridge-universal-apple-darwin`, which must itself
// be a fat binary. So all three files are produced: both arch builds and their
// `lipo -create` merge (lipo ships with the Xcode command line tools, macOS
// only).
//
// The Windows compatibility contract applies here: the sidecar carries a `.exe`
// suffix on Windows and process spawn/signal differ; the Rust supervisor owns
// the platform stop semantics.

import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const desktopDir = resolve(here, "..");
const repoRoot = resolve(desktopDir, "..", "..");
const outDir = resolve(desktopDir, "src-tauri", "binaries");

const BASE = "bnbloc-bridge";
const PKG = "./services/bridge/cmd";

// A macOS universal bundle needs one fat sidecar named for the universal
// triple, merged from these per-arch builds.
export const UNIVERSAL_MAC = "universal-apple-darwin";
export const MAC_ARCH_TRIPLES = ["aarch64-apple-darwin", "x86_64-apple-darwin"];

// Map a Rust target triple to Go's GOOS/GOARCH and the executable suffix.
// An unrecognized triple is an error: silently building linux/amd64 for it
// would ship a sidecar that cannot run.
export function goEnvForTriple(triple) {
  let goos;
  if (triple.includes("apple") || triple.includes("darwin")) goos = "darwin";
  else if (triple.includes("windows")) goos = "windows";
  else if (triple.includes("linux")) goos = "linux";

  let goarch;
  if (triple.startsWith("aarch64") || triple.startsWith("arm64")) goarch = "arm64";
  else if (triple.startsWith("x86_64")) goarch = "amd64";
  else if (triple.startsWith("i686") || triple.startsWith("i586")) goarch = "386";

  if (!goos || !goarch) {
    throw new Error(`unsupported target triple for the bridge sidecar: ${triple}`);
  }
  return { goos, goarch, exeSuffix: goos === "windows" ? ".exe" : "" };
}

// sidecarFileName is the externalBin file Tauri looks up for a triple.
export function sidecarFileName(base, triple) {
  const suffix = triple === UNIVERSAL_MAC ? "" : goEnvForTriple(triple).exeSuffix;
  return `${base}-${triple}${suffix}`;
}

// sidecarPlan lists the Go builds for a requested triple and, for the macOS
// universal triple, the lipo merge that produces the fat file.
export function sidecarPlan(triple, base = BASE) {
  if (triple === UNIVERSAL_MAC) {
    const builds = MAC_ARCH_TRIPLES.map((t) => ({ triple: t, file: sidecarFileName(base, t), ...goEnvForTriple(t) }));
    return { builds, merge: { file: sidecarFileName(base, UNIVERSAL_MAC), parts: builds.map((b) => b.file) } };
  }
  return { builds: [{ triple, file: sidecarFileName(base, triple), ...goEnvForTriple(triple) }], merge: null };
}

// requestedTriple resolves the target: --target, then Tauri's env, then host.
export function requestedTriple(argv, env, host) {
  const i = argv.indexOf("--target");
  if (i >= 0) {
    const v = (argv[i + 1] || "").trim();
    if (!v || v.startsWith("--")) throw new Error("--target needs a Rust target triple");
    return v;
  }
  const eq = argv.find((a) => a.startsWith("--target="));
  if (eq) return eq.slice("--target=".length).trim();
  if (env.TAURI_ENV_TARGET_TRIPLE) return env.TAURI_ENV_TARGET_TRIPLE.trim();
  return host();
}

function rustHostTriple() {
  const out = execFileSync("rustc", ["-vV"], { encoding: "utf8" });
  const line = out.split("\n").find((l) => l.startsWith("host:"));
  if (!line) throw new Error("could not determine host target triple from rustc -vV");
  return line.slice("host:".length).trim();
}

// Build identity stamped into the sidecar (internal/buildinfo, logged at bridge
// start): CI_COMMIT_SHA, else the checkout's HEAD; the build time is now.
const BUILDINFO_PKG = "github.com/luckylhb90/bnbloc/internal/buildinfo";

export function buildIdentityLdflags(commit, builtAt) {
  return `-X ${BUILDINFO_PKG}.Commit=${commit} -X ${BUILDINFO_PKG}.BuiltAt=${builtAt}`;
}

function buildCommit() {
  const fromEnv = (process.env.CI_COMMIT_SHA || "").trim();
  if (fromEnv) return fromEnv;
  try {
    return execFileSync("git", ["rev-parse", "--short=12", "HEAD"], { cwd: repoRoot, encoding: "utf8" }).trim();
  } catch {
    return "";
  }
}

function goBuild({ triple, file, goos, goarch }, goTags, ldflags) {
  const outPath = resolve(outDir, file);
  const buildArgs = ["build", "-trimpath", "-ldflags", ldflags];
  if (goTags) buildArgs.push("-tags", goTags);
  buildArgs.push("-o", outPath, PKG);
  console.log(
    `[sidecar] building ${BASE} for ${triple} (GOOS=${goos} GOARCH=${goarch}${goTags ? `, tags=${goTags}` : ""})`,
  );
  execFileSync("go", buildArgs, {
    cwd: repoRoot,
    stdio: "inherit",
    env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0", GOFLAGS: "-mod=mod" },
  });
  console.log(`[sidecar] wrote ${outPath}`);
}

function main() {
  const triple = requestedTriple(process.argv.slice(2), process.env, rustHostTriple);
  const plan = sidecarPlan(triple);
  if (plan.merge && process.platform !== "darwin") {
    throw new Error(`${UNIVERSAL_MAC} needs lipo (Xcode command line tools); build it on macOS`);
  }
  mkdirSync(outDir, { recursive: true });
  const goTags = (process.env.GO_TAGS || "").trim();
  const ldflags = buildIdentityLdflags(buildCommit(), new Date().toISOString().replace(/\.\d+Z$/, "Z"));
  for (const b of plan.builds) goBuild(b, goTags, ldflags);
  if (plan.merge) {
    const fat = resolve(outDir, plan.merge.file);
    execFileSync("lipo", ["-create", "-output", fat, ...plan.merge.parts.map((p) => resolve(outDir, p))], {
      stdio: "inherit",
    });
    console.log(`[sidecar] wrote ${fat}`);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
