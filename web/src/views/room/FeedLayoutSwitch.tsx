import type { FeedLayout } from "@/lib/wire.gen";
import { Segment, type SegmentOption } from "@/ui/Segment";

const OPTIONS: readonly SegmentOption<FeedLayout>[] = [
  { value: "time", label: "By time" },
  { value: "request", label: "By request" },
];

// FeedLayoutSwitch is the feed's header: how the member reads the
// conversation (docs/prd/2026-10-21-feed-by-request.md), every message in
// arrival order or one block per request. The choice is theirs on every
// device (useFeedLayout).
export function FeedLayoutSwitch({ layout, onChange }: { readonly layout: FeedLayout; readonly onChange: (layout: FeedLayout) => void }) {
  return (
    <div data-feed-header className="flex shrink-0 justify-end px-4 pt-2 @5xl:px-6">
      <Segment size="sm" ariaLabel="Show the conversation" options={OPTIONS} value={layout} onChange={onChange} />
    </div>
  );
}
