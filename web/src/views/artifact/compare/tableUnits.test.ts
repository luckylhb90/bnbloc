import { columnName, tableUnits } from "./tableUnits.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq([columnName(1), columnName(26), columnName(27)], ["A", "Z", "AA"], "column names");
const got = tableUnits("Region,Q1,Q2\nNorth,120,\nSouth,95,130\n", { extension: "csv" }, 100);
eq(got.units.map((u) => [u.key, u.label, u.text]), [
  ["A1", "A1", "Region"], ["B1", "B1", "Q1"], ["C1", "C1", "Q2"],
  ["A2", "A2 · Region", "North"], ["B2", "B2 · Q1", "120"],
  ["A3", "A3 · Region", "South"], ["B3", "B3 · Q1", "95"], ["C3", "C3 · Q2", "130"],
], "cells, keyed by address, empty cells skipped");
eq(tableUnits("a,b,c\n1,2,3", { extension: "csv" }, 4), { units: tableUnits("a,b,c\n1,2,3", { extension: "csv" }, 4).units, truncated: true }, "bounded");
eq(tableUnits("a\tb", { extension: "tsv" }, 10).units.length, 2, "tab-separated");
