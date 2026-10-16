import { isWholeColumns, MAX_RANGE_KEYS, rangeDetail, rangeKeys, rangeLabel, spanCells } from "./tableRange.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const rows = [
  ["Region", "Q1", "Q2"],
  ["North", "120", "140"],
  ["South", "95", "130"],
  ["West", "80", "88"],
];

eq(spanCells({ r: 3, c: 2 }, { r: 1, c: 0 }), { r0: 1, c0: 0, r1: 3, c1: 2 }, "a span reads either way round");
eq(rangeLabel({ r0: 2, c0: 1, r1: 2, c1: 1 }, 4), "B3", "one cell");
eq(rangeLabel({ r0: 1, c0: 1, r1: 3, c1: 2 }, 4), "B2:C4", "a range");
eq(rangeLabel({ r0: 0, c0: 1, r1: 3, c1: 1 }, 4), "Column B", "a whole column");
eq(rangeLabel({ r0: 0, c0: 1, r1: 3, c1: 2 }, 4), "Columns B:C", "whole columns");
eq(rangeLabel({ r0: 0, c0: 0, r1: 0, c1: 0 }, 1), "A1", "a one-row table's cell is a cell");
eq(isWholeColumns({ r0: 0, c0: 0, r1: 3, c1: 0 }, 4), true, "all rows");
eq(isWholeColumns({ r0: 1, c0: 0, r1: 3, c1: 0 }, 4), false, "not from the first row");
eq(rangeLabel({ r0: 0, c0: 26, r1: 0, c1: 26 }, 1), "AA1", "columns past Z");

// The addresses are Compare's: row 1 is the header.
eq(rangeKeys({ r0: 1, c0: 0, r1: 2, c1: 1 }), ["A2", "B2", "A3", "B3"], "every address, empty or not");
eq(rangeKeys({ r0: 0, c0: 0, r1: MAX_RANGE_KEYS, c1: 0 }), undefined, "past the limit: none");
eq(rangeKeys({ r0: 0, c0: 0, r1: MAX_RANGE_KEYS - 1, c1: 0 })?.length, MAX_RANGE_KEYS, "exactly the limit");

eq(rangeDetail(rows, { r0: 1, c0: 1, r1: 2, c1: 2 }, true), "Columns: B = Q1, C = Q2\n120\t140\n95\t130", "columns named, values as TSV");
eq(rangeDetail(rows, { r0: 1, c0: 1, r1: 2, c1: 2 }, false), "120\t140\n95\t130", "no header row, no names");
const tall = Array.from({ length: 100 }, (_, i) => [String(i)]);
eq(rangeDetail(tall, { r0: 0, c0: 0, r1: 99, c1: 0 }, false).split("\n").at(-1), "… and 60 more rows", "bounded rows are said");
const wide = [Array.from({ length: 30 }, (_, i) => String(i))];
eq(rangeDetail(wide, { r0: 0, c0: 0, r1: 0, c1: 29 }, false).split("\n").at(-1), "… and 10 more columns", "bounded columns are said");
