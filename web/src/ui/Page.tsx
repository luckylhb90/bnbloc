export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: React.ReactNode;
}) {
  return (
    // Narrow screens: the actions wrap below the title instead of squeezing
    // it or running off the edge.
    <div className="sticky top-0 z-10 flex flex-wrap items-center justify-between gap-x-4 gap-y-3 border-b border-[var(--color-border)] bg-[var(--color-bg)]/80 px-6 py-4 backdrop-blur">
      <div className="min-w-0 flex-[1_1_12rem]">
        <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && (
          <p className="mt-0.5 truncate text-sm text-[var(--color-fg-muted)]">
            {subtitle}
          </p>
        )}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  hint,
  action,
}: {
  icon: React.ReactNode;
  title: string;
  hint?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-20 text-center animate-in">
      <div className="grid size-14 place-items-center rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-fg-subtle)]">
        {icon}
      </div>
      <div>
        <p className="font-medium text-[var(--color-fg)]">{title}</p>
        {hint && (
          <p className="mt-1 text-sm text-[var(--color-fg-muted)] max-w-[42ch]">
            {hint}
          </p>
        )}
      </div>
      {action}
    </div>
  );
}
