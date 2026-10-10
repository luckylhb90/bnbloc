import { useId, useRef } from "react";
import { XIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { Button } from "./Button";
import { useModalLayer } from "./useModalLayer";

export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = "md",
  dismissible = true,
}: {
  open: boolean;
  onClose: () => void;
  title?: string;
  description?: string;
  children?: React.ReactNode;
  footer?: React.ReactNode;
  size?: "sm" | "md" | "lg";
  dismissible?: boolean;
}) {
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  const descriptionId = useId();
  useModalLayer(surfaceRef, { open, onEscape: dismissible ? onClose : undefined });

  if (!open) return null;
  const w = { sm: "max-w-sm", md: "max-w-md", lg: "max-w-2xl" }[size];
  // On a phone-width viewport, content dialogs fill the screen (inside the
  // safe areas); a small confirm stays a centered card.
  const fullOnNarrow = size !== "sm";

  return (
    <div className={cn("fixed inset-0 z-50 flex items-center justify-center p-4", fullOnNarrow && "max-sm:p-0")}>
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm animate-in"
        onClick={dismissible ? onClose : undefined}
      />
      <div
        ref={surfaceRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        className={cn(
          "animate-in relative flex max-h-[85vh] focus:outline-none w-full flex-col rounded-[var(--radius-xl)] border border-[var(--color-border-strong)]",
          "bg-[var(--color-surface)] shadow-[var(--shadow-diffuse)]",
          "shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]",
          w,
          fullOnNarrow &&
            "max-sm:h-[var(--app-height,100dvh)] max-sm:max-h-none max-sm:max-w-none max-sm:rounded-none max-sm:border-0 max-sm:pt-[env(safe-area-inset-top)] max-sm:pb-[env(safe-area-inset-bottom)]",
        )}
      >
        {dismissible && (
          <button
            onClick={onClose}
            data-layer-close
            className={cn(
              "tactile absolute right-4 top-4 z-10 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
              fullOnNarrow && "max-sm:top-[calc(1rem+env(safe-area-inset-top))]",
            )}
            aria-label="Close dialog"
          >
            <XIcon size={18} />
          </button>
        )}
        {(title || description) && (
          <div className="shrink-0 px-6 pt-6 pb-2 pr-12">
            {title && (
              <h2 id={titleId} className="text-lg font-semibold tracking-tight text-[var(--color-fg)]">
                {title}
              </h2>
            )}
            {description && (
              <p id={descriptionId} className="mt-1 text-sm text-[var(--color-fg-muted)] leading-relaxed">
                {description}
              </p>
            )}
          </div>
        )}
        {children && <div className="min-h-0 flex-1 overflow-y-auto px-6 py-3">{children}</div>}
        {footer && (
          <div className="shrink-0 flex justify-end gap-2 px-6 pb-6 pt-3">{footer}</div>
        )}
      </div>
    </div>
  );
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmLabel = "Confirm",
  danger,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  description?: string;
  confirmLabel?: string;
  danger?: boolean;
}) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant={danger ? "danger" : "primary"}
            onClick={() => {
              onConfirm();
              onClose();
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    />
  );
}
