import { useState } from "react";
import { inviteCodeComplete, normalizeInviteCode } from "@/lib/inviteCode";
import { isSubmitEnter } from "@/lib/keys";
import { InviteCodeLen } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Input } from "@/ui/Input";

// JoinPanel is how a person who is no member joins a room: one click for a
// shared room, the invite code for a private one (docs/prd/2026-10-22-short-
// invite-codes.md). The caller's onJoin does the join (and says why it
// failed): the code, when the room wants one.
export function JoinPanel({
  needsCode,
  joining,
  onJoin,
}: {
  readonly needsCode: boolean;
  readonly joining: boolean;
  readonly onJoin: (code?: string) => void;
}) {
  const [code, setCode] = useState("");
  if (!needsCode) {
    return (
      <Button variant="primary" onClick={() => onJoin()} disabled={joining}>
        {joining ? "Joining…" : "Join room"}
      </Button>
    );
  }
  const ready = inviteCodeComplete(code);
  const submit = () => ready && !joining && onJoin(code);
  return (
    <div className="flex items-center gap-2">
      <Input
        aria-label="Invite code"
        placeholder={"X".repeat(InviteCodeLen)}
        value={code}
        maxLength={InviteCodeLen}
        autoCapitalize="characters"
        autoComplete="off"
        spellCheck={false}
        className="tabular w-28 text-center uppercase tracking-widest"
        onChange={(e) => setCode(normalizeInviteCode(e.target.value))}
        onKeyDown={(e) => isSubmitEnter(e) && submit()}
      />
      <Button variant="primary" onClick={submit} disabled={!ready || joining}>
        {joining ? "Joining…" : "Join with code"}
      </Button>
    </div>
  );
}
