import { useCallback } from "react";
import { copyText } from "@/lib/clipboard";
import { useToast } from "@/ui/Toast";

// useCopy copies text and says so: "<What> copied" only when the copy
// happened, "Could not copy the <what>" when it did not. Resolves whether
// it copied.
export function useCopy(): (text: string, what: string) => Promise<boolean> {
  const toast = useToast();
  return useCallback(
    (text: string, what: string) =>
      copyText(text).then(
        () => {
          toast.success(`${what.charAt(0).toUpperCase()}${what.slice(1)} copied`);
          return true;
        },
        () => {
          toast.error(`Could not copy the ${what}`);
          return false;
        },
      ),
    [toast],
  );
}
