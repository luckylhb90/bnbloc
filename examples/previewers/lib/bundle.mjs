// Shared build for installable previewer bundles (bnbloc.previewer.package/v1,
// runtime "bundle"): one esbuild bundle of src/main.ts plus static files,
// packed as a deterministic ustar archive with its package template.
//
//   dist/site/<files>         the bundle files (inspect / serve), with
//                             NOTICES.txt when main.js inlines packages
//   dist/bundle.tar           uncompressed POSIX ustar, deterministic
//   dist/bundle.tar.sha256    the archive's sha256 (hex)
//   dist/package.json         previewer.json, artifacts.bundle left as the
//                             upload placeholder {url:"", sha256:""}
//
// Install: `bnbloc previewers publish --package dist/package.json --bundle
// dist/bundle.tar [--unsigned]` uploads the archive to the gateway, pins its
// url + sha256 into the package, signs and installs it.
//
// Needs only Node 24 and the web client's installed dependencies (esbuild
// comes through vite, `pnpm --dir web install`).

import { createHash } from "node:crypto";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { bundledPackageRoots, dependencyRoots, notices } from "./notices.mjs";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "web");

/** esbuild is not a direct web dependency: resolve it through vite. */
function loadEsbuild() {
  const fromWeb = createRequire(join(WEB, "package.json"));
  try {
    return fromWeb("esbuild");
  } catch {
    return createRequire(fromWeb.resolve("vite/package.json"))("esbuild");
  }
}

// --- ustar writer ----------------------------------------------------------

const BLOCK = 512;

function octal(value, width) {
  // width-1 octal digits + NUL terminator.
  return value.toString(8).padStart(width - 1, "0") + "\0";
}

function tarHeader(path, size) {
  const header = Buffer.alloc(BLOCK, 0);
  if (Buffer.byteLength(path) > 100) throw new Error(`tar: path too long for this writer: ${path}`);
  header.write(path, 0, 100, "utf8");
  header.write(octal(0o644, 8), 100, 8, "ascii"); // mode
  header.write(octal(0, 8), 108, 8, "ascii"); // uid
  header.write(octal(0, 8), 116, 8, "ascii"); // gid
  header.write(octal(size, 12), 124, 12, "ascii"); // size
  header.write(octal(0, 12), 136, 12, "ascii"); // mtime
  header.write("        ", 148, 8, "ascii"); // checksum placeholder (spaces)
  header.write("0", 156, 1, "ascii"); // typeflag: regular file
  header.write("ustar\0", 257, 6, "ascii"); // magic
  header.write("00", 263, 2, "ascii"); // version
  header.write(octal(0, 8), 329, 8, "ascii"); // devmajor
  header.write(octal(0, 8), 337, 8, "ascii"); // devminor
  let sum = 0;
  for (const byte of header) sum += byte;
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  return header;
}

/** Packs {path: Buffer} into a deterministic ustar archive (sorted paths,
 *  mode 0644, mtime 0, uid/gid 0, two zero end blocks). */
export function packTar(files) {
  const parts = [];
  for (const path of Object.keys(files).sort()) {
    const data = files[path];
    parts.push(tarHeader(path, data.length), data);
    const pad = (BLOCK - (data.length % BLOCK)) % BLOCK;
    if (pad) parts.push(Buffer.alloc(pad, 0));
  }
  parts.push(Buffer.alloc(BLOCK * 2, 0));
  return Buffer.concat(parts);
}

// --- build -----------------------------------------------------------------

/** Builds the previewer in dir: src/main.ts bundled to main.js, the static
 *  files named in assets (paths relative to dir), and NOTICES.txt with the
 *  licenses of every third-party package main.js holds (title names the
 *  previewer there). prebuilt names bundled packages whose browser build
 *  already inlines their own dependencies: those are attributed too.
 *  esbuild options extend the defaults (iife, es2020, minified). */
export async function buildBundle({ dir, title, assets, prebuilt = [], outDir = join(dir, "dist"), quiet = false, esbuild = {} }) {
  const result = await loadEsbuild().build({
    entryPoints: [join(dir, "src", "main.ts")],
    bundle: true,
    format: "iife",
    target: "es2020",
    minify: true,
    // License headers inside bundled code (e.g. what a dependency's own
    // dist inlines) stay, collected at the end of main.js.
    legalComments: "eof",
    write: false,
    logLevel: "silent",
    metafile: true,
    absWorkingDir: dir,
    ...esbuild,
  });
  const files = { "main.js": Buffer.from(result.outputFiles[0].contents) };
  for (const name of assets) files[name] = await readFile(join(dir, name));
  const packages = bundledPackageRoots(Object.keys(result.metafile.inputs), dir);
  const inlined = new Set();
  for (const root of [...packages]) {
    const { name } = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
    if (!prebuilt.includes(name)) continue;
    for (const dep of await dependencyRoots(root)) if (!packages.includes(dep)) inlined.add(dep);
  }
  const notice = await notices([...packages, ...[...inlined].sort()], title, inlined);
  if (notice) files["NOTICES.txt"] = notice;
  const template = JSON.parse(await readFile(join(dir, "previewer.json"), "utf8"));
  if (!files[template.previewer.entry]) throw new Error(`entry ${template.previewer.entry} is not in the bundle`);

  const tar = packTar(files);
  const sha256 = createHash("sha256").update(tar).digest("hex");
  // The exact upload placeholder: the package parser rejects a sha256 without
  // its url, and the publish CLI pins both after the upload.
  const pkg = { ...template, artifacts: { bundle: { url: "", sha256: "" } } };

  await rm(outDir, { recursive: true, force: true });
  await mkdir(join(outDir, "site"), { recursive: true });
  for (const [path, data] of Object.entries(files)) await writeFile(join(outDir, "site", path), data);
  const tarPath = join(outDir, "bundle.tar");
  const packagePath = join(outDir, "package.json");
  await writeFile(tarPath, tar);
  await writeFile(`${tarPath}.sha256`, `${sha256}\n`);
  await writeFile(packagePath, JSON.stringify(pkg, null, 2) + "\n");
  if (!quiet) {
    console.log(`bundle   ${tarPath} (${tar.length} bytes)`);
    console.log(`sha256   ${sha256}`);
    console.log(`package  ${packagePath}`);
    console.log(`site     ${join(outDir, "site")}`);
  }
  return { sha256, tarPath, packagePath, siteDir: join(outDir, "site"), files: Object.keys(files).sort(), packages };
}
