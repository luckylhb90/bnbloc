# bnbloc web design system

## 1. Product character

bnbloc is an operational collaboration console: dense, durable, and calm. The UI should feel like a trusted desktop control plane, not a marketing surface.

## 2. Tokens

Source of truth is `web/src/styles.css` `@theme`.

- Background: `--color-bg` for the app canvas.
- Surfaces: `--color-surface`, `--color-surface-2`, and `--color-elevated` for cards, controls, and raised active states.
- Borders: `--color-border` and `--color-border-strong` for quiet separation.
- Text: `--color-fg`, `--color-fg-muted`, and `--color-fg-subtle`.
- Action: `--color-accent`, `--color-accent-hover`, `--color-accent-fg`, and `--color-accent-soft`.
- Risk: `--color-danger` and `--color-warning`.
- Radius: `--radius`, `--radius-lg`, `--radius-xl`.
- Shadows: `--shadow-pop` for active controls, `--shadow-diffuse` for dialogs.

## 3. Typography

Use the configured sans stack for UI copy and `tabular` for versions, ids, counters, timestamps, and byte progress. Headings use tight tracking and medium/semibold weight; helper text stays small and muted.

## 4. Layout and adaptation

Prefer centered content columns (`max-w-[720px]` for settings/detail pages, `max-w-4xl` for admin dashboards). Adapt to container stage via `useStage`; do not branch on device width. Coarse pointer hit targets are provided by `.tap` and `.coarse-targets`.

## 5. Primitives and states

- `Button`: primary for committed actions, outline/secondary for neutral operations, danger for destructive or forced-risk actions, ghost for dismiss/cancel.
- `Input`, `Textarea`, `Field`: all form controls use these primitives so labels, hints, and field errors stay consistent.
- `Card`, `Badge`, `Dot`: group operational state; use warning/danger badges for rollout risk and forced updates.
- `Segment`, `Switch`: use segmented controls for small enums and switches for boolean policy.
- `Dialog`: capped to `max-h-[85vh]`; long content scrolls in the body while header/footer stay pinned.
- `Toast`: use for transient save/check outcomes; persistent update availability also needs an on-page/dialog affordance.

## 6. Motion and interaction

Use `.tactile` for controls and `.animate-in` only when content enters because of user or async state. Motion must communicate affordance or state; no decorative loops except existing status pulses.

## 7. Accessibility and copy

Plain English, no i18n layer. Label every input with a visible `Field` label. Avoid ambiguous success/failure states: update checks must explicitly say up to date, available, or failed.
