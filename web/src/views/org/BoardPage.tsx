import { useAuth } from "@/lib/auth";
import { can } from "@/lib/permissions";
import { useActingTenant } from "@/lib/session";
import { OrgPulseTab } from "@/views/org/OrgPulseTab";

// BoardPage is the organization board as a destination for every member. The
// server aggregates it (people and countries under 5 are withheld), and the
// board is keyed by the acting organization so no figure carries over from
// another one.
export function BoardPage() {
  const { tenant, role } = useAuth();
  const { acting } = useActingTenant(can(role, "manage_platform"));
  const activeTenant = acting ?? tenant ?? undefined;
  return <OrgPulseTab key={activeTenant} />;
}
