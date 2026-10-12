import { feedSeqRange } from "./feedSeqRange.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const r = feedSeqRange([{ seq: 12 }, {}, { seq: 7 }, { seq: 30 }, { seq: 0 }]);
assert(r !== null && r.min === 7 && r.max === 30, "range over loaded rows");
assert(feedSeqRange([]) === null && feedSeqRange([{}, { seq: 0 }]) === null, "nothing loaded: null");
