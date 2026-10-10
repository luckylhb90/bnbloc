import { forwardRef } from "react";
import { cn } from "@/lib/cn";

export const Input = forwardRef<
  HTMLInputElement,
  React.InputHTMLAttributes<HTMLInputElement>
>(({ className, ...props }, ref) => (
  <input
    ref={ref}
    className={cn(
      "h-10 w-full rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 text-sm",
      "border border-[var(--color-border)] text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)]",
      "transition-colors focus:outline-none focus:border-[var(--color-accent)] focus:bg-[var(--color-surface)]",
      className,
    )}
    {...props}
  />
));
Input.displayName = "Input";

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  React.TextareaHTMLAttributes<HTMLTextAreaElement>
>(({ className, ...props }, ref) => (
  <textarea
    ref={ref}
    className={cn(
      "w-full rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 py-2 text-sm resize-none",
      "border border-[var(--color-border)] text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)]",
      "transition-colors focus:outline-none focus:border-[var(--color-accent)] focus:bg-[var(--color-surface)]",
      className,
    )}
    {...props}
  />
));
Textarea.displayName = "Textarea";

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-2">
      <span className="text-xs font-medium text-[var(--color-fg-muted)] uppercase tracking-wide">
        {label}
      </span>
      {children}
      {hint && !error && (
        <span className="text-xs text-[var(--color-fg-subtle)]">{hint}</span>
      )}
      {error && <span className="text-xs text-[var(--color-danger)]">{error}</span>}
    </label>
  );
}
