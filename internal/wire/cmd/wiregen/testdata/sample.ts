
// ---- sample/sample.go ----

// Kind is a named string type: the union of its constants.
export type Kind = "a" | "b";

// Kinds.
export const KindA = "a";
// KindB is documented.
export const KindB = "b";

// Limits are plain constants.
export const MaxBytes = 8388608;
export const Ratio = 0.5;
export const Enabled = true;

// Event kinds collected into one union across blocks.
export const EventOne = "one";
export const EventTwo = "two";

export const EventThree = "three";

// Base is embedded.
export interface Base {
  id: string;
}

// Item shows field forms.
export interface Item extends Base {
  // Name is required.
  name: string;
  note?: string; // trailing comment
  kind: Kind;
  tags: string[];
  counts?: Record<string, number>;
  byKind?: Partial<Record<Kind, number>>;
  parent: Base | null;
  child?: Base;
  raw: unknown;
  any: unknown;
  bytes: string;
  role: Kind;
  roles: Kind[];
  big: string;
  inline: { X: number; };
  Untagged: number;
}

// Kinds lists every kind in display order.
export const Kinds: readonly Kind[] = ["a", "b"];

// Spec is a registry row.
export interface Spec {
  kind: string;
  durable: boolean;
}

// Specs is a registry of constant rows.
export const Specs: readonly Spec[] = [{ kind: "one", durable: true }, { kind: "two", durable: false }];

// ByName is a constant map.
export const ByName: Readonly<Record<string, number>> = { one: 1 };

// Alias is a slice type.
export type Alias = Item[];

// ---- sample unions (//wire:union) ----
export type EventKind = "one" | "two" | "three";
export const EventKindValues: readonly EventKind[] = ["one", "two", "three"];
