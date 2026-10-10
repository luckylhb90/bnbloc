import { useCallback, useRef } from "react";

// useFeedScroll keeps the conversation scroller pinned to the latest message
// while the member is near the bottom, and keeps the viewport anchored when an
// older history page is prepended. Content can grow without a feed change
// (a reply animating in, images and diagrams loading), so growth is observed
// on the content element rather than inferred from feed updates.
export function useFeedScroll() {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const atBottom = useRef(true);
  const prepend = useRef<{ readonly height: number; readonly top: number } | null>(null);
  const observer = useRef<ResizeObserver | null>(null);

  const settle = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (prepend.current) {
      const { height, top } = prepend.current;
      prepend.current = null;
      el.scrollTop = el.scrollHeight - height + top;
      return;
    }
    if (atBottom.current) el.scrollTop = el.scrollHeight;
  }, []);

  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (el) atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
  }, []);

  // Called by the history loader right before it prepends an older page.
  const beforePrepend = useCallback(() => {
    const el = scrollRef.current;
    if (el) prepend.current = { height: el.scrollHeight, top: el.scrollTop };
  }, []);

  // Callback ref for the scroller's content wrapper: observes its size for as
  // long as it is mounted (the chat column unmounts while Files is open).
  const contentRef = useCallback(
    (node: HTMLDivElement | null) => {
      observer.current?.disconnect();
      observer.current = null;
      if (!node || typeof ResizeObserver === "undefined") return;
      observer.current = new ResizeObserver(settle);
      observer.current.observe(node);
    },
    [settle],
  );

  // settle: the room runs it in a layout effect on every feed change (new
  // rows, a prepended page) so the position is fixed before paint.
  return { scrollRef, contentRef, onScroll, beforePrepend, settle };
}
