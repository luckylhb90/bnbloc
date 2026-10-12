import rows from "../../../internal/membership/testdata/abilities.json" with { type: "json" };
import { canInvite, isHuman, isOwner, mayChat } from "./roomRoles.ts";

// The fixture the Go owner (internal/membership.Member) is tested against:
// the web mirror answers every row alike.
for (const row of rows) {
  const m = { role: row.role, agentHost: row.agentHost };
  if (isHuman(m) !== row.isHuman || mayChat(m) !== row.mayChat) {
    throw new Error(`${JSON.stringify(row)}: isHuman=${isHuman(m)} mayChat=${mayChat(m)}`);
  }
}
if (mayChat({ role: undefined }) || isHuman({ role: "" })) {
  throw new Error("no membership may neither chat nor count as a person");
}

// canInvite reads store.Role.CanInvite (RoomRolesThatInvite, generated);
// only the owner edits, transfers or deletes the room.
const expect: Record<string, [boolean, boolean]> = { owner: [true, true], admin: [true, false], member: [false, false], visitor: [false, false], "": [false, false] };
for (const [role, [invite, owner]] of Object.entries(expect)) {
  if (canInvite({ role }) !== invite || isOwner({ role }) !== owner) throw new Error(`${role}: canInvite=${canInvite({ role })} isOwner=${isOwner({ role })}`);
}
if (canInvite({ role: "owner", agentHost: true })) throw new Error("an agent host is never a moderator");
