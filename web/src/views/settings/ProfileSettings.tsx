import { useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import { defaultDisplayName, validateDisplayName } from "@/lib/displayNames";
import { MaxDisplayNameRunes } from "@/lib/wire.gen";
import { setPrivacyMode, usePrivacyMode } from "@/lib/privacyMode";
import { profilesApi } from "@/lib/profilesApi";
import { nameResolver } from "@/lib/useDisplayName";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// ProfileSettings is Settings → Profile (the member's display name, PRD
// 2026-10-10 §1) and Settings → Privacy (the local "Hide email addresses"
// toggle, §2).
export function ProfileSettings() {
  const { subject } = useAuth();
  const toast = useToast();
  const [saved, setSaved] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const privacy = usePrivacyMode();

  useEffect(() => {
    if (!subject) return;
    let live = true;
    profilesApi
      .mine()
      .then((p) => {
        if (!live) return;
        // The profile answers with the default applied; an unset name shows
        // as an empty field with the default as its placeholder.
        const name = p?.displayName?.trim() ?? "";
        const own = name && name !== defaultDisplayName(subject) ? name : "";
        setSaved(own);
        setDraft(own);
        nameResolver.set(subject, name || null);
      })
      .catch(() => live && setSaved(""));
    return () => {
      live = false;
    };
  }, [subject]);

  const problem = validateDisplayName(draft);
  const dirty = saved !== null && draft.trim() !== saved;

  const save = async () => {
    if (!subject || problem || !dirty) return;
    const next = draft.trim();
    const prev = saved;
    setSaving(true);
    // Optimistic: every rendering of this member updates at once.
    nameResolver.set(subject, next || null);
    try {
      const p = await profilesApi.save(next);
      const name = p?.displayName?.trim() || next;
      nameResolver.set(subject, name || null);
      setSaved(next);
      setDraft(next);
      toast.success(next ? "Display name saved" : "Display name cleared");
    } catch (e) {
      nameResolver.set(subject, prev || null);
      toast.error(errorText(e, "Could not save your display name"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-semibold">Profile</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
            The name other people see on your messages, agents and in member lists.
          </p>
        </div>
        <Card className="px-4 py-3">
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <label htmlFor="profile-display-name" className="text-sm font-medium">
              Display name
            </label>
            <div className="flex items-center gap-2">
              <Input
                id="profile-display-name"
                value={draft}
                maxLength={MaxDisplayNameRunes * 2}
                placeholder={subject ? defaultDisplayName(subject) : ""}
                disabled={saved === null || saving}
                onChange={(e) => setDraft(e.target.value)}
                aria-invalid={!!problem}
              />
              <Button type="submit" variant="primary" disabled={!dirty || !!problem || saving}>
                {saving ? "Saving…" : "Save"}
              </Button>
            </div>
            <p className={problem ? "text-xs text-[var(--color-danger)]" : "text-xs text-[var(--color-fg-subtle)]"}>
              {problem ?? `Up to ${MaxDisplayNameRunes} characters. Leave it empty to use the part of your sign-in before the @.`}
            </p>
          </form>
        </Card>
      </section>
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-semibold">Privacy</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">For demos and screenshots. Applies to this browser only.</p>
        </div>
        <Card>
          <label className="flex cursor-pointer items-center justify-between gap-4 px-4 py-3">
            <span className="min-w-0">
              <span className="block text-sm font-medium">Hide email addresses</span>
              <span className="block text-xs text-[var(--color-fg-muted)]">
                Show display names wherever an email address would appear. Nothing is hidden from the server.
              </span>
            </span>
            <input
              type="checkbox"
              role="switch"
              className="size-4 accent-[var(--color-accent)]"
              checked={privacy}
              onChange={(e) => setPrivacyMode(e.target.checked)}
              aria-label="Hide email addresses"
            />
          </label>
        </Card>
      </section>
    </>
  );
}
