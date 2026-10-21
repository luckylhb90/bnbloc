# The browser saw a menu it could not click (2026-10-21)

Found on QA with browser 0.7.3.

## What happened

A member asked an agent to run a query in an internal database console
(Vue 3 with a component library's side menu). The agent read the menu's
items in the page text but could not open one: its controls were only the
menu search box, the collapse button, an avatar link, two inputs and an
"enlarge" button. It tried the search box with Enter, Tab and the arrow
keys, collapsed and expanded the side bar, and guessed five addresses
(`/sqlquery`, `/#/sqlquery`, ...), all wrong, then asked the member to
click the menu for it.

## Root cause

`snapshot.js` listed only native controls (`a[href]`, `button`, `input`,
`select`, `textarea`, `summary`, `contenteditable`) and elements with a
control `role`. The console's menu items are `<li class="…-menu-item">`
and `<div class="…-submenu-title">` with the click bound by the framework
and navigation by its router: no `href`, `role`, `tabindex` or `onclick`.
Their only outward sign is `cursor: pointer`. Component-library admin
pages (menus, tabs, cards, table row actions) are full of such elements.

## Fix (browser 0.7.4)

The snapshot also lists an element a script made clickable: the outermost
element showing the pointer cursor (its parent does not), or one with an
`onclick` attribute, that has a name, is not inside a listed control, and
holds no listed control (a card with its own buttons keeps the buttons).
It is a `button` with its text (whitespace folded, at most 120 characters)
as its label, clicked and hit-tested like any control. A `<label>` is left
to the control it names, unless that control is hidden (a styled checkbox
whose input is transparent), where the label is the only way to it.

Test: `TestScriptClickableControlsOnChrome` (a framework-style menu of
`li`/`div` items routed by script, a card with a button inside, a label
over a transparent checkbox, and a pointer `span` inside a button).

Checked read-only on the QA page itself: the whole menu (46 items), the
dashboard's cards and table rows are listed, 96 controls in all; the
snapshot took 16.6 ms on its 2,767 elements (one computed style each,
cached).

## Why it was not caught

- Lens: External contracts (the pages the component reads are not ours).
- Missing question or test: the reference snapshot and its fixture page
  use native controls only; nothing checked a page built with a component
  library, where the click is a script's and the element says nothing.
- Added to docs/design-review.md: lens 9 asks whether a reader of
  third-party pages was tried on one built the common way for that kind
  of page, not only on the reference's fixture.
