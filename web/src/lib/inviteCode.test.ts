import { inviteCodeComplete, normalizeInviteCode } from "./inviteCode.ts";

const cases: Array<[string, string]> = [
  ["ab7k", "AB7K"],
  [" ab-7 k ", "AB7K"],
  ["abcdefg", "ABCD"],
  ["o0il1", ""], // symbols a code never has
  ["", ""],
];
for (const [raw, want] of cases) {
  if (normalizeInviteCode(raw) !== want) throw new Error(`${JSON.stringify(raw)} -> ${normalizeInviteCode(raw)}, want ${want}`);
}
if (!inviteCodeComplete("AB7K") || inviteCodeComplete("AB7") || inviteCodeComplete("")) throw new Error("inviteCodeComplete");
