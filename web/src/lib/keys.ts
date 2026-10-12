// Keyboard rules shared by every text field (web M18).

interface KeyLike {
  readonly key: string;
  readonly shiftKey?: boolean;
  readonly keyCode?: number;
  readonly nativeEvent?: { readonly isComposing?: boolean };
}

// isImeKey: a key pressed while an input method composes text (CJK input)
// belongs to the IME. Its Enter confirms the composition and must never
// send. keyCode 229 covers Safari, which reports the confirming keydown
// after compositionend.
export function isImeKey(e: KeyLike): boolean {
  return !!e.nativeEvent?.isComposing || e.keyCode === 229;
}

// isSubmitEnter: Enter that submits — no Shift (Shift+Enter is a newline),
// not an IME confirmation.
export function isSubmitEnter(e: KeyLike): boolean {
  return e.key === "Enter" && !e.shiftKey && !isImeKey(e);
}
