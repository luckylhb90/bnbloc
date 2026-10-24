import { pageSource } from "./pageSource.ts";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(msg);
}

// A page names its service, the installed version and the build running.
assert(pageSource({ component: "connections" }) === "connections", "no version");
assert(pageSource({ component: "connections", version: "0.2.0" }) === "connections 0.2.0", "installed version");
assert(pageSource({ component: "connections", version: "0.2.0", serviceVersion: "v0.6.0" }) === "connections 0.2.0 · running v0.6.0", "and the build");
assert(pageSource({ component: "c", version: "1.0.0", serviceVersion: "1.0.0" }) === "c 1.0.0", "the same once");
console.log("pageSource ok");
