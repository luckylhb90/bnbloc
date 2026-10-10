// startPointerDrag runs one pointer-driven resize: every pointermove reports
// the event, release ends it. Text selection is suspended for the drag so a
// fast pointer never paints a selection across the page. Shared by the
// live-preview dock and the people-chat panel.
export function startPointerDrag(
  e: { preventDefault(): void },
  onMove: (ev: PointerEvent) => void,
  onEnd: () => void,
): void {
  e.preventDefault();
  document.body.style.userSelect = "none";
  const up = () => {
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", up);
    document.body.style.userSelect = "";
    onEnd();
  };
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", up);
}
