// NOTICES.txt for a previewer bundle that inlines third-party packages: the
// license and notice files of every package esbuild put into main.js, read
// from the installed copies so the text always matches what the bundle holds.

import { existsSync, readFileSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join, sep } from "node:path";

const LICENSE_FILE = /^(licen[cs]e|notice|copying|copyright|third[-_]party[-_]notices)([-._].*)?$/i;

/** The package roots of the bundled inputs (absolute paths up to and
 *  including node_modules/<name> or node_modules/@scope/<name>). */
export function bundledPackageRoots(inputs, cwd) {
  const roots = new Set();
  for (const input of inputs) {
    const abs = input.startsWith(sep) ? input : join(cwd, input);
    const at = abs.lastIndexOf(`${sep}node_modules${sep}`);
    if (at < 0) continue;
    const rest = abs.slice(at + 14).split(sep);
    const n = rest[0].startsWith("@") ? 2 : 1;
    roots.add(abs.slice(0, at + 14) + rest.slice(0, n).join(sep));
  }
  return [...roots].sort();
}

/** The roots of root's runtime dependencies, recursively (for a package
 *  that ships a prebuilt bundle of them, which esbuild sees as one file). */
export async function dependencyRoots(root) {
  const out = new Set();
  const walk = async (dir) => {
    const { dependencies = {} } = JSON.parse(await readFile(join(dir, "package.json"), "utf8"));
    const req = createRequire(join(dir, "package.json"));
    for (const name of Object.keys(dependencies)) {
      const dep = packageRoot(req, name);
      if (!dep || out.has(dep)) continue;
      out.add(dep);
      await walk(dep);
    }
  };
  await walk(root);
  return [...out].sort();
}

/** The root of name as resolved from req: up from its entry to the
 *  package.json that names it (not every package exports ./package.json);
 *  null when it is not installed. */
function packageRoot(req, name) {
  let entry;
  try {
    entry = req.resolve(name);
  } catch {
    try {
      entry = req.resolve(`${name}/package.json`);
    } catch {
      return null;
    }
  }
  for (let d = dirname(entry); d !== dirname(d); d = dirname(d)) {
    const pkg = join(d, "package.json");
    if (existsSync(pkg) && JSON.parse(readFileSync(pkg, "utf8")).name === name) return d;
  }
  return null;
}

/** Every license/notice file of a package root: at its top level and in a
 *  licenses/ directory. A bundled package without one fails the build. */
async function licenseFiles(root) {
  const top = (await readdir(root, { withFileTypes: true })).filter((e) => e.isFile() && LICENSE_FILE.test(e.name)).map((e) => e.name);
  let extra = [];
  try {
    extra = (await readdir(join(root, "licenses"), { withFileTypes: true })).filter((e) => e.isFile()).map((e) => `licenses/${e.name}`);
  } catch {}
  return [...top.sort(), ...extra.sort()];
}

/** NOTICES.txt for the bundled package roots; null when nothing third-party
 *  is bundled. A root in inlined (a dependency a prebuilt bundle carries)
 *  without a license file is named with its package.json license instead
 *  of failing the build. */
export async function notices(roots, title, inlined = new Set()) {
  if (roots.length === 0) return null;
  const parts = [`Third-party software bundled in main.js of the bnbloc ${title}.\n`];
  const seen = new Set();
  for (const root of roots) {
    const { name, version, license } = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
    if (seen.has(`${name}@${version}`)) continue;
    seen.add(`${name}@${version}`);
    const files = await licenseFiles(root);
    if (files.length === 0 && inlined.has(root)) {
      parts.push(`${"=".repeat(78)}\n${name}@${version} — license: ${typeof license === "string" ? license : "see package"} (no license file shipped)\n${"=".repeat(78)}\n`);
      continue;
    }
    if (files.length === 0) throw new Error(`${name}@${version} is bundled but ships no license file`);
    for (const file of files) {
      const text = await readFile(join(root, file), "utf8");
      parts.push(`${"=".repeat(78)}\n${name}@${version} — ${file}\n${"=".repeat(78)}\n\n${text.trimEnd()}\n`);
    }
  }
  return Buffer.from(parts.join("\n"));
}
