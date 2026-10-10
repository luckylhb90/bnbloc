import { columnWidths, detectDelimiter, parseDelimited } from "./tableData.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

eq(parseDelimited('a,b\n1,"x, ""y"""\n\n2,3\r\n', ",").rows, [["a", "b"], ["1", 'x, "y"'], ["2", "3"]], "quotes, blank lines, CRLF");
eq(parseDelimited('h\n"multi\nline"', ",").rows, [["h"], ["multi\nline"]], "quoted newline");
eq(parseDelimited("﻿a\tb\n1\t2", "\t").rows, [["a", "b"], ["1", "2"]], "BOM + tabs");
const capped = parseDelimited("1\n2\n3\n4\n", ",", 2);
eq([capped.rows.length, capped.truncated], [2, true], "row cap");
eq(detectDelimiter("a\tb\tc\n", {}), "\t", "tab detected");
eq(detectDelimiter("a;b;c\n", {}), ";", "semicolon detected");
eq(detectDelimiter("a,b\n", { extension: "tsv" }), "\t", "tsv by type");
eq(columnWidths([["id", "a long description of something"]]), [64, 253], "widths size by content");
