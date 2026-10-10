import { createContext, useCallback, useContext, useMemo, useState } from "react";
import { CheckCircle, WarningCircle, Info, X } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";

type ToastTone = "success" | "error" | "info";
interface Toast {
  id: number;
  tone: ToastTone;
  message: string;
}

const ToastCtx = createContext<(tone: ToastTone, message: string) => void>(
  () => {},
);

// useToast returns a stable object (keyed on the provider's stable push) so
// effects and callbacks may list it as a dependency without re-running on
// every render.
export function useToast() {
  const push = useContext(ToastCtx);
  return useMemo(
    () => ({
      success: (m: string) => push("success", m),
      error: (m: string) => push("error", m),
      info: (m: string) => push("info", m),
    }),
    [push],
  );
}

let seq = 1;

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const push = useCallback((tone: ToastTone, message: string) => {
    const id = seq++;
    setToasts((t) => [...t, { id, tone, message }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4200);
  }, []);

  const dismiss = (id: number) => setToasts((t) => t.filter((x) => x.id !== id));

  const icon = {
    success: <CheckCircle size={18} weight="fill" className="text-[var(--color-accent)]" />,
    error: <WarningCircle size={18} weight="fill" className="text-[var(--color-danger)]" />,
    info: <Info size={18} weight="fill" className="text-[var(--color-fg-muted)]" />,
  };

  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div
        role="region"
        aria-label="Notifications"
        aria-live="polite"
        className="fixed bottom-4 right-4 z-[60] flex flex-col gap-2 w-[min(92vw,22rem)]"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            role={t.tone === "error" ? "alert" : undefined}
            className={cn(
              "animate-in flex items-start gap-3 rounded-[var(--radius)] border px-4 py-3",
              "border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]",
            )}
          >
            <span className="mt-0.5" aria-hidden="true">{icon[t.tone]}</span>
            <span className="flex-1 text-sm text-[var(--color-fg)] leading-snug">
              {t.message}
            </span>
            <button
              type="button"
              onClick={() => dismiss(t.id)}
              aria-label="Dismiss notification"
              title="Dismiss"
              className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
            >
              <X size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}
