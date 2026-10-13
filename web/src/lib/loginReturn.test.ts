import { beginShareSignIn, LOGIN_RETURN_TTL_MS, takeLoginReturn, type ReturnStore } from "./loginReturn.ts";

function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

function memoryStore(): ReturnStore & { value: string } {
  const s = {
    value: "",
    read: () => s.value,
    write: (v: string) => {
      s.value = v;
    },
  };
  return s;
}

const token = "bsh_0123456789abcdef0123456789abcdef_c2VjcmV0LXNlY3JldA";

test("a share page sends the person to the app's sign-in and is brought back after it", () => {
  const store = memoryStore();
  eq(beginShareSignIn(token, `https://bnbloc.example.com/#/share/${token}`, 1_000, store), "https://bnbloc.example.com/", "the sign-in URL");
  eq(beginShareSignIn(token, `https://bnbloc.example.com/app/index.html?x=1#/share/${token}`, 1_000, store), "https://bnbloc.example.com/app/index.html", "a document base");
  eq(takeLoginReturn(2_000, store), `#/share/${token}`, "back to the share page");
  eq(takeLoginReturn(2_001, store), null, "only once");
});

test("an expired, foreign or malformed return goes nowhere", () => {
  const store = memoryStore();
  beginShareSignIn(token, "https://bnbloc.example.com/", 0, store);
  eq(takeLoginReturn(LOGIN_RETURN_TTL_MS + 1, store), null, "expired");
  eq(store.value, "", "an expired return is cleared");
  for (const raw of ['{"hash":"#/room/r1","atMs":0}', '{"hash":"https://evil.example/","atMs":0}', "not json", '{"hash":1,"atMs":0}']) {
    store.value = raw;
    eq(takeLoginReturn(1, store), null, `refuses ${raw}`);
  }
  eq(beginShareSignIn("a/b", "https://bnbloc.example.com/", 0, store), null, "a token no share page takes");
});
