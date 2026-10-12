import { createContext, useCallback, useContext, useMemo, useState } from "react";
import { CheckCircle, WarningCircle, Info, X } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";

type ToastTone = "success" | "error" | "info";

// ToastAction is an optional button on a toast ("Open in Library"); a toast
// with actions stays up longer so there is time to use them.
export interface ToastAction {
  readonly label: string;
  readonly onClick: () => void;
}

interface Toast {
  id: number;
  tone: ToastTone;
  message: string;
  actions?: readonly ToastAction[];
}

type Push = (tone: ToastTone, message: string, actions?: readonly ToastAction[]) => void;

const ToastCtx = createContext<Push>(() => {});

// useToast returns a stable object (keyed on the provider's stable push) so
// effects and callbacks may list it as a dependency without re-running on
// every render.
export function useToast() {
  const push = useContext(ToastCtx);
  return useMemo(
    () => ({
      success: (m: string, actions?: readonly ToastAction[]) => push("success", m, actions),
      error: (m: string, actions?: readonly ToastAction[]) => push("error", m, actions),
      info: (m: string, actions?: readonly ToastAction[]) => push("info", m, actions),
    }),
    [push],
  );
}

let seq = 1;

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const push = useCallback<Push>((tone, message, actions) => {
    const id = seq++;
    setToasts((t) => [...t, { id, tone, message, actions: actions?.length ? actions : undefined }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), actions?.length ? 10000 : 4200);
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
            <span className="flex flex-1 flex-col gap-2 text-sm text-[var(--color-fg)] leading-snug">
              {t.message}
              {t.actions && (
                <span className="flex flex-wrap gap-2">
                  {t.actions.map((a) => (
                    <button
                      key={a.label}
                      type="button"
                      onClick={() => {
                        dismiss(t.id);
                        a.onClick();
                      }}
                      className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-0.5 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]"
                    >
                      {a.label}
                    </button>
                  ))}
                </span>
              )}
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
