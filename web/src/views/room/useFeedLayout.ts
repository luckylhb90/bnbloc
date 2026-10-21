import { useCallback, useState } from "react";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { profilesApi } from "@/lib/profilesApi";
import { queryKey, useQuery } from "@/lib/useQuery";
import { FeedLayoutTime, type FeedLayout } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";

const MY_PREFERENCES = "my-preferences";

// useFeedLayout is how the member reads a room's conversation, By time or
// By request (docs/prd/2026-10-21-feed-by-request.md §3.1): the choice is
// stored with their account (GET/PUT /v1/my/preferences), so every device
// follows it. Until it loads, and when it cannot be read, the feed reads By
// time (the server's default too). A choice shows at once; a failed save
// puts the stored one back and says so.
export function useFeedLayout(): { readonly layout: FeedLayout; readonly setLayout: (layout: FeedLayout) => void } {
  const { subject, tenant } = useAuth();
  const toast = useToast();
  const prefs = useQuery(queryKey(MY_PREFERENCES, { subject, tenant }), profilesApi.preferences, { staleMs: 60_000 });
  const [picked, setPicked] = useState<FeedLayout | null>(null);
  const { refetch } = prefs;
  const setLayout = useCallback(
    (layout: FeedLayout) => {
      setPicked(layout);
      void profilesApi
        .savePreferences({ feedLayout: layout })
        .catch((e: unknown) => toast.error(errorText(e, "Could not save how this feed reads")))
        .then(() => refetch())
        // A later pick made meanwhile stays until its own save settles.
        .finally(() => setPicked((cur) => (cur === layout ? null : cur)));
    },
    [toast, refetch],
  );
  return { layout: picked ?? prefs.data?.feedLayout ?? FeedLayoutTime, setLayout };
}
