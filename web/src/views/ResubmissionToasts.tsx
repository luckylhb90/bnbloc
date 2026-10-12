import { useEffect } from "react";
import { contentResubmitted } from "@/lib/contentComponentsApi";
import { resubmittedMessage } from "@/lib/contentSharing";
import { useToast } from "@/ui/Toast";

// ResubmissionToasts tells the author when saving a new version of a
// component their organization uses sent it to the admins for approval
// (the gateway opens that request by itself). Mounted once, beside the app.
export function ResubmissionToasts() {
  const toast = useToast();
  useEffect(
    () =>
      contentResubmitted.subscribe((r) => {
        const msg = resubmittedMessage(r);
        if (msg) toast.info(msg);
      }),
    [toast],
  );
  return null;
}
