// The one clipboard write (web M23). navigator.clipboard exists only in a
// secure context, so a gateway served over plain http falls back to a
// hidden textarea + execCommand("copy"). Rejects when neither works, so a
// caller never reports a copy that did not happen.
export async function copyText(text: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  try {
    ta.select();
    if (!document.execCommand("copy")) throw new Error("copy is not available here");
  } finally {
    ta.remove();
  }
}
