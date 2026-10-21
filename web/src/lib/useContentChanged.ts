import { useEffect, useRef } from "react";
import { contentChanges, touches } from "./contentChanges.ts";
import type { ContentKind } from "./wire.gen.ts";

// useContentChanged runs onChange whenever a content change concerns one of
// kinds (lib/contentChanges.ts): how a list of content follows every
// mutation without each view reloading by hand. The latest onChange is
// always used (no re-subscribe per render).
export function useContentChanged(kinds: readonly ContentKind[], onChange: () => void): void {
  const ref = useRef(onChange);
  ref.current = onChange;
  const key = kinds.join(",");
  useEffect(() => {
    const watched = key.split(",") as ContentKind[];
    return contentChanges.subscribe((change) => {
      if (touches(change, watched)) ref.current();
    });
  }, [key]);
}
