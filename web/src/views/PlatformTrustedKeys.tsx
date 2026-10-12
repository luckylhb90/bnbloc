import { useEffect, useState } from "react";
import { ShieldCheckIcon, TrashIcon, WarningCircleIcon } from "@phosphor-icons/react";
import { api, type ComponentKey } from "@/lib/api";
import { Button } from "@/ui/Button";
import { ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

// Trusted publisher keys are the market's root of trust: install refuses any
// package signed by a key not listed here. So the empty state is a warning, not
// a neutral "nothing yet", and removal is confirmed.
export function TrustedKeysPanel() {
  const toast = useToast();
  const [keys, setKeys] = useState<ComponentKey[] | null>(null);
  const [publicKey, setPublicKey] = useState("");
  const [label, setLabel] = useState("");
  const [addError, setAddError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [pendingRemove, setPendingRemove] = useState<ComponentKey | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .listComponentKeys()
      .then((r) => !cancelled && setKeys(r.keys))
      .catch(() => !cancelled && toast.error("Failed to load trusted keys"));
    return () => {
      cancelled = true;
    };
  }, [toast]);

  const add = async () => {
    if (!publicKey.trim() || adding) return;
    setAdding(true);
    setAddError(null);
    try {
      const added = await api.addComponentKey({ publicKey: publicKey.trim(), label: label.trim() || undefined });
      setKeys((prev) => [added, ...(prev ?? [])]);
      setPublicKey("");
      setLabel("");
      toast.success(`Trusted ${added.keyId}`);
    } catch (e) {
      // 400 (unparseable) and 409 (duplicate) both belong beside the key field;
      // the server's message is specific and must reach the operator verbatim.
      setAddError(errorText(e, "Failed to add key"));
    } finally {
      setAdding(false);
    }
  };

  const remove = async () => {
    if (!pendingRemove) return;
    try {
      await api.removeComponentKey(pendingRemove.keyId);
      setKeys((prev) => (prev ?? []).filter((k) => k.keyId !== pendingRemove.keyId));
      toast.success("Publisher key removed");
    } catch (e) {
      toast.error(errorText(e, "Failed to remove key"));
    }
  };

  return (
    <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex items-center gap-2">
        <ShieldCheckIcon size={18} weight="fill" className="text-[var(--color-accent)]" />
        <h3 className="text-sm font-semibold text-[var(--color-fg)]">Trusted publisher keys</h3>
      </div>

      {keys && keys.length === 0 && (
        <div className="mt-3 flex items-start gap-2 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3">
          <WarningCircleIcon size={16} weight="fill" className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
          <p className="text-sm leading-relaxed text-[var(--color-fg-muted)]">
            No trusted keys yet. Until one is added, every signed package is refused at install. Add a
            publisher key to enable the market.
          </p>
        </div>
      )}

      {keys && keys.length > 0 && (
        <ul className="mt-3 flex flex-col gap-2">
          {keys.map((k) => (
            <li
              key={k.keyId}
              className="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-[var(--color-fg)]">{k.label || "unlabeled key"}</span>
                  <span className="tabular text-xs text-[var(--color-fg-subtle)]">{k.keyId}</span>
                </div>
                {k.addedAtMs ? (
                  <p className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">
                    Added {new Date(k.addedAtMs).toLocaleString()}
                    {k.addedBy ? (
                      <>
                        {" "}by <UserName id={k.addedBy} />
                      </>
                    ) : null}
                  </p>
                ) : null}
              </div>
              <button
                type="button"
                onClick={() => setPendingRemove(k)}
                aria-label={`Remove ${k.keyId}`}
                className="tap tactile grid size-8 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger)]/10 hover:text-[var(--color-danger)]"
              >
                <TrashIcon size={16} />
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="mt-4 flex flex-col gap-2 border-t border-[var(--color-border)] pt-4">
        <label className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
          Add a minisign public key
        </label>
        <textarea
          value={publicKey}
          onChange={(e) => {
            setPublicKey(e.target.value);
            setAddError(null);
          }}
          rows={3}
          placeholder={"untrusted comment: minisign public key: …\nRW…"}
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2 font-mono text-xs text-[var(--color-fg)] focus:border-[var(--color-accent)] focus:outline-none"
        />
        <input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Label (optional), e.g. Acme Corp"
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-sm text-[var(--color-fg)] focus:border-[var(--color-accent)] focus:outline-none"
        />
        {addError && <p className="text-sm text-[var(--color-danger)]">{addError}</p>}
        <div>
          <Button variant="primary" size="sm" onClick={add} disabled={adding || !publicKey.trim()} className="tap">
            {adding ? "Adding…" : "Trust this key"}
          </Button>
        </div>
      </div>

      <ConfirmDialog
        open={pendingRemove !== null}
        onClose={() => setPendingRemove(null)}
        onConfirm={remove}
        title="Remove trusted key"
        description={`Remove publisher key ${pendingRemove?.keyId ?? ""}? New packages signed by it will be refused. Packages already installed stay installed — they were verified when they were added.`}
        confirmLabel="Remove"
        danger
      />
    </div>
  );
}
