// A JSON value's address, the way a member (and an agent) would write it:
// $.items[2].name, $["odd key"]. Pure, unit-tested under node.

const IDENTIFIER = /^[A-Za-z_$][\w$]*$/;

/** The path of a child of `parent` ("$" for the document). */
export function childPath(parent: string, key: string, parentIsArray: boolean): string {
  if (parentIsArray) return `${parent}[${key}]`;
  return IDENTIFIER.test(key) ? `${parent}.${key}` : `${parent}[${JSON.stringify(key)}]`;
}

/** A scalar as the detail shows it, cut so a long string stays a line. */
export function valueText(value: unknown, limit = 500): string {
  const text = JSON.stringify(value) ?? String(value);
  const chars = [...text];
  return chars.length > limit ? `${chars.slice(0, limit - 1).join("")}…` : text;
}
