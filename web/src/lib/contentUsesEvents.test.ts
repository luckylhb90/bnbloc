import { contentUsesChanged } from "./contentUsesEvents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const seen: string[] = [];
const off = contentUsesChanged.subscribe((id) => seen.push(id));
contentUsesChanged.emit("room-1");
off();
contentUsesChanged.emit("room-2");
assert(seen.join(",") === "room-1", `listeners see emits until they unsubscribe: ${seen}`);
