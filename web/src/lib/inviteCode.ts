import { InviteCodeAlphabet, InviteCodeLen } from "./wire.gen.ts";

// What a person types as a room's invite code becomes the code: upper case,
// only the symbols a code has (a space or a dash read out between letters is
// dropped, and 0, O, 1, I, L are not in it), at most its length. The server
// draws codes from the same alphabet (wire.InviteCodeAlphabet).
export function normalizeInviteCode(raw: string): string {
  let out = "";
  for (const ch of raw.toUpperCase()) {
    if (InviteCodeAlphabet.includes(ch)) out += ch;
  }
  return out.slice(0, InviteCodeLen);
}

// complete: the typed code is as long as a code is.
export function inviteCodeComplete(code: string): boolean {
  return code.length === InviteCodeLen;
}
