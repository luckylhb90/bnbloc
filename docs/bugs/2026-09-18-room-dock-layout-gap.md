Status: fixed (98699e6)

## Bug

Opening the room live-preview dock can leave a large blank gap between the
collapsed channel rail and the chat feed, while the dock's rigid width squeezes
the chat column until long unbroken ids overflow message bubbles.

## Repro

1. Sign in to the web client.
2. Open a room with chat messages and an artifact live preview.
3. Open the preview dock on the right.
4. Resize across desktop widths.

Expected: the chat feed starts after the rail with only normal room padding,
long ids wrap inside bubbles, and the page has no horizontal overflow.

Actual: the feed is centered inside a fixed 1400px wrapper, producing a large
left gutter when the dock narrows the chat area; the dock uses a fixed persisted
pixel width and does not yield gracefully.

## Root cause

`RoomView` centers the loading, message feed, and composer wrappers with
`mx-auto max-w-[1400px]`. When the dock opens, the chat column shrinks but the
feed remains centered, splitting spare space into left and right margins and
visually detaching it from the rail. The dock also renders from a hard pixel
width and `shrink-0`, so it keeps its requested size before the chat can adapt.

## Fix plan

- Replace the fixed centered chat wrappers with a fluid, left-aligned reading
  measure owned by the chat column.
- Make the dock render from a fluid CSS clamp around the user's persisted
  preference so drag-to-resize remains, but the dock yields to available space.
- Add wrapping/min-width constraints so unbroken tokens break inside message
  bubbles instead of forcing horizontal overflow.

## Status

Fixed in the working tree. `RoomView` now left-aligns chat content with a
container-owned fluid reading measure, stores dock size as a row-relative share,
renders the dock with a CSS clamp, and wraps unbroken message tokens inside the
available bubble width.
