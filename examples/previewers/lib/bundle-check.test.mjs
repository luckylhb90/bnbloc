// node --test examples/previewers/lib/bundle-check.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";

import { NETWORK_CALLS } from "./bundle-check.mjs";

test("the network check finds the global fetch and nothing named like it", () => {
  for (const code of ['fetch("/x")', "await fetch(u,{})", "fetch(u).then(r=>{})", "window.fetch(u)", "globalThis.fetch(u)", "new WebSocket(u)", "new XMLHttpRequest()"]) {
    assert.match(code, NETWORK_CALLS, code);
  }
  // A bundled TeX parser has a method called fetch (called and defined).
  for (const code of ["n.fetch().text", "this.fetch()", "fetch(){return this.nextToken}", "fetch(a,b){return a}", "myfetch(u)", "prefetch(u)"]) {
    assert.doesNotMatch(code, NETWORK_CALLS, code);
  }
});
