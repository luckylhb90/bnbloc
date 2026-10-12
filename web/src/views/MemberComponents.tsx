import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type MemberComponent } from "@/lib/api";
import { Card } from "@/ui/Card";
import { PageHeader } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { ComponentCard } from "@/views/ComponentCard";
import { ErrorBox } from "@/ui/ErrorBox";
import { errorText } from "@/lib/errors";

export function MemberComponents() {
  const toast = useToast();
  const [components, setComponents] = useState<readonly MemberComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const res = await api.components();
      setComponents(res.components ?? []);
    } catch (caught: unknown) {
      setError(errorText(caught, "failed to load components"));
      setComponents([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const groups = useMemo(() => splitComponents(components ?? []), [components]);

  const install = async (component: MemberComponent) => {
    setBusy(component.name);
    setError(null);
    try {
      await api.installComponent(component);
      toast.success(`${component.name} installed`);
      await load();
    } catch (caught: unknown) {
      setError(errorText(caught, `failed to install ${component.name}`));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (component: MemberComponent) => {
    setBusy(component.name);
    setError(null);
    try {
      await api.uninstallComponent(component);
      toast.success(`${component.name} removed`);
      await load();
    } catch (caught: unknown) {
      setError(errorText(caught, `failed to remove ${component.name}`));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <PageHeader
        title="Components"
        subtitle="Components distributed by your admin. Managed components stay active; opt-in components are your choice."
      />
      <div className="p-6">
        <div className="mx-auto flex w-full max-w-[720px] flex-col gap-3">
          {error && <ErrorBox>{error}</ErrorBox>}
          <ComponentSection title="Your components" loading={components === null} emptyTitle="No components installed." emptyHint="Add an opt-in component below, or ask your admin to auto-install one.">
            {groups.yours.map((component) => (
              <ComponentCard
                key={`${component.kind}:${component.name}`}
                mode="member"
                component={component}
                busy={busy === component.name}
                onRemove={(selected) => void remove(selected)}
              />
            ))}
          </ComponentSection>
          <ComponentSection title="Available to add" loading={false} emptyTitle="Nothing available to add." emptyHint="Your admin has not published opt-in components for this workspace.">
            {groups.available.map((component) => (
              <ComponentCard
                key={`${component.kind}:${component.name}`}
                mode="member"
                component={component}
                busy={busy === component.name}
                onInstall={(selected) => void install(selected)}
              />
            ))}
          </ComponentSection>
        </div>
      </div>
    </>
  );
}

function splitComponents(components: readonly MemberComponent[]) {
  return {
    yours: components.filter((component) => component.installed),
    available: components.filter((component) => !component.installed),
  };
}

function ComponentSection({
  title,
  loading,
  emptyTitle,
  emptyHint,
  children,
}: {
  readonly title: string;
  readonly loading: boolean;
  readonly emptyTitle: string;
  readonly emptyHint: string;
  readonly children: React.ReactNode;
}) {
  const hasChildren = Array.isArray(children) ? children.length > 0 : Boolean(children);
  return (
    <section className="flex flex-col gap-2">
      <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">{title}</span>
      {loading ? <EmptyCard title="Loading components…" hint="Fetching your member catalog." /> : hasChildren ? children : <EmptyCard title={emptyTitle} hint={emptyHint} />}
    </section>
  );
}

function EmptyCard({ title, hint }: { readonly title: string; readonly hint: string }) {
  return (
    <Card className="border-dashed px-4 py-8 text-center">
      <p className="text-sm font-medium text-[var(--color-fg)]">{title}</p>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{hint}</p>
    </Card>
  );
}
