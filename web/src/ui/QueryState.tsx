import { ErrorBox } from "./ErrorBox";
import { Button } from "./Button";

// The states of a loaded list (web M20): loading, failed, or empty. A load
// error is never shown as an empty list ("No experts yet"), and "Loading…"
// ends when the request does.

// listPlaceholder is a list's placeholder line while it has no rows.
export function listPlaceholder(rows: readonly unknown[] | null | undefined, failed: boolean, empty: string): string {
  if (rows) return empty;
  return failed ? "Could not load this list." : "Loading…";
}

// LoadFailed is a first load that failed: why, and a way to try again.
export function LoadFailed({ message, onRetry }: { readonly message: string; readonly onRetry: () => void }) {
  return (
    <ErrorBox>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span>{message}</span>
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      </div>
    </ErrorBox>
  );
}
