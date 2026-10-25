// hostList reads a hosts field (comma, space or one per line):
// lower-cased, de-duplicated.
export function hostList(s: string): string[] {
  return [...new Set(s.split(/[,\s]+/).map((h) => h.trim().toLowerCase()).filter(Boolean))];
}
