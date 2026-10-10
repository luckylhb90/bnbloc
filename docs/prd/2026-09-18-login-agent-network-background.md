# Login page: agent-network map background

**Date:** 2026-09-18
**Status:** landed

## Intent

Give the login page a premium ambient background: a tiled "map" grid over
which agent nodes light up one by one and then link to each other with drawn
lines — a visual metaphor for members' agents joining a shared room.

Originally scoped to the left brand panel only. On review the effect was
required to be **full-screen**, so the canvas was promoted to a single
full-viewport layer behind the entire login grid (see Revision below).

## Design constraints

- Canvas-based (`<canvas>` + `requestAnimationFrame`), no new dependencies.
- Uses only the existing theme tokens (`--color-accent`, `--color-border`,
  `--color-fg-subtle`, etc.) — desaturated teal on zinc, no neon.
- Sits *behind* the radial accent gradient and all copy; headline, paragraph
  and the sign-in form stay fully readable (a centre-weighted mask plus
  edge gradients fade the effect under both text zones).
- Respects `prefers-reduced-motion` (render a static lit state, no animation).
- Pauses when the tab is hidden; DPR-aware backing store.
- Canvas box is **CSS-driven** (`absolute inset-0`, no inline px size), so it
  reflows fluidly and can never impose a pixel footprint on layout.
- Node count scales with viewport area (clamped 24–52) so the composition
  reads as intentional at both ~900px and ~1680px wide.

## Revision — full-bleed cutover

The first implementation mounted the canvas inside the brand panel, which is
`hidden` below `lg`. Two problems followed:

1. The effect was confined to the left column — not the full screen.
2. `sizeCanvas()` wrote its measured width/height back into
   `canvas.style.width/height`. Per the clean-cutover rule the inline sizing
   was removed outright rather than guarded, and CSS now owns the box.

Changes:

- `Login.tsx`: outer wrapper became `relative … overflow-hidden`; the canvas
  is the first child, spanning the whole grid. The brand panel's opaque
  `bg-[var(--color-surface)]` became a translucent `/30` tint (plus a right
  border + inset sheen) so the shared canvas shows through while the panel
  still reads as a distinct editorial surface.
- Because the brand panel is `hidden` below `lg`, the headline/paragraph would
  have disappeared on small screens while the map is now visible there. The
  pitch was extracted into one `Pitch` component rendered in both places
  (`compact` variant inside the sign-in card) — one source of truth, no
  hand-copied duplicate.
- The sub-`lg` sign-in panel gained a translucent card so the form stays
  legible over the now-visible background.
- Mask re-authored from right-weighted to centre-weighted, falling off under
  both the left headline and the right form.

## Acceptance

- Login page renders the animated map across the full viewport with no console
  errors (a pre-existing `/favicon.ico` 404 aside); text remains legible.
- No horizontal overflow: `documentElement.scrollWidth === clientWidth`, and
  the "Continue" button stays inside the viewport, across a resize sequence
  (1680→1400→1200→1024→900→1680→1100).
- `cd web && pnpm build` passes.
- Reduced-motion users see a static, lit network.

## Verification

Measured in headless Chrome on the real dev server, with a page-identity
guard asserting the bnbloc login actually mounted (an earlier run produced a
false PASS against an unrelated app on a neighbouring Vite port):

| viewport | scrollW / clientW | grid columns | canvas width |
| --- | --- | --- | --- |
| 1680x1000 | 1680 / 1680 | 880px 800px | 1680 |
| 1280x800 | 1280 / 1280 | — | 1280 |
| 1024x768 | 1024 / 1024 | 536.375px 487.625px | 1024 |
| 900x700 | 900 / 900 | 900px (single col) | 900 |

Screenshots at 1680x1000, 1280x800 and 900x700 confirm the network spans the
full width including behind the sign-in side, with "Welcome back", the email
input and the "Continue" button fully visible and uncropped.

## Landing

- Component: `web/src/views/LoginBackground.tsx`
- Mount + `Pitch` extraction: `web/src/views/Login.tsx`
