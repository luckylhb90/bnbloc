# Stage-driven multi-platform room layout

Status: implemented (98699e6)

## Intent

The room UI must serve phone, tablet, desktop, the 720px Tauri minimum window,
and desktop windows narrowed by an open dock from one layout rule set. The goal
is full feature parity on mobile without adding device-specific compatibility
branches.

## Principle

Branch on capabilities, not device names:

- **Available room**: measure the room row container and choose a stage.
- **Pointer precision**: use pointer/hover media capability for tap targets and
  hover-only affordances.
- **Local platform APIs**: keep using the existing Tauri capability check for
  desktop-only preview pop-out behavior.

This avoids treating an iPad, a shrunken desktop window, and a phone as the same
or different for the wrong reasons. A wide touch device can keep a wide layout;
a narrow mouse window gets the compact layout it can actually afford.

## Stage thresholds

Stages use `web/src/lib/capability.ts`:

- `compact`: less than `34rem`
- `medium`: at least `34rem`, less than `58rem`
- `wide`: at least `58rem`

The thresholds are rem-based so browser zoom and user font-size preferences
change the cutover. A user at 200% zoom should receive the denser/safer stage
earlier because the interface genuinely has less usable room in text units.

## Room behavior

- **Wide**: channel rail, chat, and live preview dock can sit side-by-side. The
  existing dock share model and `bnbloc.dockShare` persistence remain the source
  of truth for inline preview width.
- **Medium**: the channel rail is an icon rail beside chat. Live preview opens as
  an overlay above the chat so it no longer steals column width.
- **Compact**: chat is the default full-width surface. The channel rail moves to
  a dismissible drawer, and live preview opens as a full-surface overlay with an
  explicit close control.

Artifact editing, room settings, member management, search, borrowing controls,
channel creation, and preview pop-out remain reachable; controls may move into
the stage-appropriate rail, drawer, or overlay, but are not removed.

## Touch and safe-area affordances

Coarse-pointer layouts apply the global `.tap` affordance to small controls so
the effective hit area is at least 44px. Hover-revealed controls use
`.hover-reveal` so touch users can reach them without relying on hover. The room
header and composer use `.pad-safe*` utilities so the notch and home indicator do
not cover the active controls.

## Status

Planned for the stage-driven RoomView cutover.
