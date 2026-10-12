import rows from "../../../internal/membership/testdata/abilities.json" with { type: "json" };
import { isHuman, mayChat } from "./roomRoles.ts";

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
