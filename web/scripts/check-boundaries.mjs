#!/usr/bin/env node
// Web boundary gate: a rule that has one owner in web/src may only be
// written in that owner's file (AGENTS.md "One rule, one owner"). Each rule
// is a pattern plus the files allowed to contain it; tests (*.test.ts) are
// exempt. check-boundaries.test.mjs runs the gate over web/src (so
// `scripts/ci.sh web` enforces it) and checks every rule catches its sample.
//
// Usage: node scripts/check-boundaries.mjs   (exits non-zero on a violation)

import { readFileSync, readdirSync, statSync } from "node:fs";
import { extname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const SRC = join(fileURLToPath(new URL(".", import.meta.url)), "..", "src");

// The type names internal/wire/cmd/wiregen generates (lib/wire.gen.ts).
const WIRE_TYPES = [...readFileSync(join(SRC, "lib", "wire.gen.ts"), "utf8").matchAll(/^export (?:interface|type) (\w+)/gm)].map((m) => m[1]);

export const RULES = [
  {
    id: "T8",
    name: "a server shape is generated from internal/wire (wire.gen.ts), never redeclared",
    // A hand-written mirror drifts from Go (web M26-M28): re-export the
    // generated type (export type { X } from "./wire.gen.ts") instead.
    pattern: new RegExp(`^export (?:interface|type) (?:${WIRE_TYPES.join("|")})\\b`, "m"),
    allowed: ["lib/wire.gen.ts"],
    sample: { file: "lib/api.ts", text: "export interface AgentJob {\n  jobId: string;\n}" },
  },
  {
    id: 1,
    name: "network requests go through the transports: req (lib/api.ts), the public share fetch, the bundle probe",
    // A hand-rolled fetch skips the session refresh, the acting tenant and
    // the error mapping (web M1).
    pattern: /\bfetch\(/,
    allowed: ["lib/session.ts", "lib/sharesApi.ts", "views/WebUpdateNotice.tsx"],
    sample: { file: "views/X.tsx", text: "const res = await fetch(url);" },
  },
  {
    id: "1b",
    name: "authenticated requests go through req; authedFetch only in the transports",
    pattern: /\bauthedFetch\(/,
    // artifactUpload streams with progress (XHR); releaseDownload fetches an
    // absolute artifact URL.
    allowed: ["lib/session.ts", "lib/api.ts", "lib/artifactUpload.ts", "lib/releaseDownload.ts"],
    sample: { file: "lib/xApi.ts", text: "const res = await authedFetch(apiUrl(path));" },
  },
  {
    id: 2,
    name: "a failed request becomes an ApiError only in the transports (lib/errors.ts owns the class)",
    pattern: /new ApiError\(/,
    allowed: ["lib/api.ts", "lib/errors.ts", "lib/sharesApi.ts", "lib/artifactUpload.ts", "lib/releaseDownload.ts"],
    sample: { file: "lib/xApi.ts", text: "if (!res.ok) throw new ApiError(res.status, await errorDetail(res));" },
  },
  {
    id: 3,
    name: "browser storage keys and access live in lib/storageKeys.ts",
    // Keys were spelled twice, unscoped by member or gateway, and wrapped in
    // four copies of the try/DOMException guard (web L1). A package or
    // contract kind ("bnbloc.x.package/v1") is not a storage key.
    pattern: /\b(?:localStorage|sessionStorage)\.|["'`]bnbloc\.(?![\w-]+\.(?:package|contract)\/)[A-Za-z]/,
    // webPush: a service-worker message type; desktopRelease: an archive name.
    allowed: ["lib/storageKeys.ts", "lib/wire.gen.ts", "lib/webPush.ts", "lib/desktopRelease.ts"],
    sample: { file: "views/X.tsx", text: 'localStorage.setItem("bnbloc.sidebarCollapsed", "1");' },
  },
  {
    id: 4,
    name: "views render a failure with errorText / statusText (lib/errors.ts)",
    // These leaked "Failed to fetch", a SyntaxError or [object Object]
    // into toasts (web M3).
    pattern: /instanceof Error \?|String\((?:e|err|error)\)/,
    scope: /^views\//,
    allowed: [],
    sample: { file: "views/X.tsx", text: 'toast.error(e instanceof Error ? e.message : "failed");' },
  },
  {
    id: 8,
    name: "role abilities are decided by lib/roomRoles.ts (room) and lib/permissions.ts (organization)",
    // Five copies of the org-role check ignored the viewer role, and the
    // room settings gear keyed on the creator, not the owner (web H7, H8).
    pattern: /\.role [!=]== "(?:owner|admin|visitor|org_admin|super_admin|viewer)"|\brole [!=]== "(?:org_admin|super_admin|viewer)"/,
    allowed: ["lib/roomRoles.ts", "lib/permissions.ts"],
    sample: { file: "views/X.tsx", text: 'const isAdmin = role === "org_admin" || role === "super_admin";' },
  },
  {
    id: 9,
    name: "an agent's handle and identity come from lib/roomAgents.ts (agentHandle, parseAgentHandle, agentIdentity, sameAgent)",
    // Four parsers split on different slashes and the builders dropped the
    // core, so two cores' agents collapsed (web M12, M13).
    pattern: /\$\{[A-Za-z_.]*[Oo]wner[A-Za-z_.]*\}\//,
    allowed: ["lib/roomAgents.ts"],
    sample: { file: "views/X.tsx", text: "<ActorLabel actor={`${job.ownerUserId}/${job.provider}`} />" },
  },
  {
    id: 5,
    name: "polling goes through lib/useVisiblePoll.ts (paused while the page is hidden)",
    // Polls ran every 2 s in a background tab (web M9). The exceptions are
    // deliberate: Shell's unread poll raises notifications while hidden, the
    // typing prune is a local timer, the update watcher follows the policy's
    // own interval.
    pattern: /setInterval\(/,
    allowed: ["lib/useVisiblePoll.ts", "views/Shell.tsx", "lib/useRoomStream.ts", "views/DesktopUpdateWatcher.tsx"],
    sample: { file: "views/X.tsx", text: "const timer = window.setInterval(() => void load(), 2000);" },
  },
  {
    id: "M10",
    name: "a cached query is keyed by queryKey / publicQueryKey (member, tenant, gateway)",
    // Hand-made keys missed the tenant, so a cached answer crossed a tenant
    // switch or a sign-out (web M10).
    pattern: /useQuery(?:<[^>]*>)?\(\s*(?:["'`]|subject\b)/,
    allowed: ["lib/useQuery.ts"],
    sample: { file: "views/X.tsx", text: "const q = useQuery(subject ? `my-agents:${subject}` : null, api.myAgents);" },
  },
  {
    id: "L2",
    name: "a room's API path is built by roomPath (lib/roomId.ts)",
    pattern: /\/v1\/rooms\/\$\{/,
    allowed: ["lib/roomId.ts"],
    sample: { file: "lib/xApi.ts", text: "req(`/v1/rooms/${encodeURIComponent(id)}/members`)" },
  },
  {
    id: 12,
    name: "what the client says about a kind lives in its Record table (COMPONENT_KINDS, CONTENT_KINDS)",
    // Label chains per kind drifted: a sandbox read "Auth provider" in
    // review, a prompt pack had no word (web M24, L5).
    pattern: /kind === "skill" \? "skill pack"|=== "bnbloc\.[\w-]+\.package\/v1" \? "[A-Z]/,
    allowed: ["lib/componentKinds.ts", "lib/contentComponents.ts"],
    sample: { file: "views/X.tsx", text: 'return kind === "bnbloc.core.package/v1" ? "Core" : "Auth provider";' },
  },
  {
    id: 6,
    name: "the session token rides a URL only on the room event stream",
    // EventSource cannot set headers, so sse.ts passes the session as
    // access_token; every other link (attachments, downloads) carries a
    // per-resource ticket instead (web H10).
    pattern: /access_token=/,
    allowed: ["lib/sse.ts"],
    sample: { file: "lib/api.ts", text: "return `${base}?access_token=${token}`;" },
  },
  {
    id: 10,
    name: "clipboard writes go through lib/clipboard.ts (ui/useCopy for the toast)",
    // navigator.clipboard is missing on an http gateway; hand-rolled copies
    // reported "copied" when nothing was (web M23).
    pattern: /navigator\.clipboard/,
    allowed: ["lib/clipboard.ts"],
    sample: { file: "views/X.tsx", text: "void navigator.clipboard?.writeText(url);" },
  },
  {
    id: "10b",
    name: "a destructive action asks through ui/Dialog ConfirmDialog, never window.confirm or alert",
    // Removals were one click or a native prompt (web M22).
    pattern: /\bwindow\.confirm\(|\balert\(/,
    allowed: [],
    sample: { file: "views/X.tsx", text: 'if (!window.confirm("Remove?")) return;' },
  },
  {
    id: "L8",
    name: "click-outside dismissal goes through ui/useDismissOnOutside",
    pattern: /addEventListener\("mousedown"/,
    allowed: ["ui/useDismissOnOutside.ts"],
    sample: { file: "views/X.tsx", text: 'document.addEventListener("mousedown", onDoc);' },
  },
];

// violations returns "<file>: rule <id> (<name>)" for every source file (a
// path relative to src, "/"-separated) breaking a rule.
export function violations(files) {
  const out = [];
  for (const { file, text } of files) {
    if (/\.test\.tsx?$/.test(file)) continue;
    for (const rule of RULES) {
      if (rule.scope && !rule.scope.test(file)) continue;
      if (rule.allowed.includes(file) || !rule.pattern.test(text)) continue;
      out.push(`${file}: rule ${rule.id} (${rule.name})`);
    }
  }
  return out;
}

export function sourceFiles(root = SRC) {
  const out = [];
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else if ([".ts", ".tsx"].includes(extname(name))) {
        out.push({ file: relative(root, path).split(sep).join("/"), text: readFileSync(path, "utf8") });
      }
    }
  };
  walk(root);
  return out;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const found = violations(sourceFiles());
  for (const v of found) console.error(v);
  process.exit(found.length > 0 ? 1 : 0);
}
