import { useEmailMask } from "@/lib/privacyMode";

// ErrorBox is the one inline error panel for admin/console views: a danger
// tinted block announced to assistive tech as an alert.
// In privacy mode an address inside a plain-text error is masked.
export function ErrorBox({ children }: { readonly children: React.ReactNode }) {
  const mask = useEmailMask();
  return (
    <div
      role="alert"
      className="rounded-[var(--radius)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 px-4 py-3 text-sm text-[var(--color-danger)]"
    >
      {typeof children === "string" ? mask(children) : children}
    </div>
  );
}
