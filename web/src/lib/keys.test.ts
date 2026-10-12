import { isImeKey, isSubmitEnter } from "./keys.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

assert(isSubmitEnter({ key: "Enter" }), "plain Enter submits");
assert(!isSubmitEnter({ key: "Enter", shiftKey: true }), "Shift+Enter is a newline");
assert(!isSubmitEnter({ key: "Enter", nativeEvent: { isComposing: true } }), "Enter confirming an IME composition never submits");
assert(!isSubmitEnter({ key: "Enter", keyCode: 229 }), "Safari's post-composition keydown never submits");
assert(!isSubmitEnter({ key: "a" }), "other keys do not submit");
assert(isImeKey({ key: "ArrowDown", nativeEvent: { isComposing: true } }), "arrows during composition belong to the IME");
assert(!isImeKey({ key: "ArrowDown" }), "arrows outside composition are the app's");
