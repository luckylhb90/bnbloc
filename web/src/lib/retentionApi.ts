import { req, scoped } from "./api.ts";
import type { PlatformRetentionView, RetentionPolicy, TenantRetentionView } from "./retention.ts";

export const retentionApi = {
  // The platform default and the cleanup status (super admins).
  platform: () => req<PlatformRetentionView>("/v1/platform/retention"),
  savePlatform: (policy: RetentionPolicy) =>
    req<PlatformRetentionView>("/v1/platform/retention", { method: "PUT", body: JSON.stringify(policy) }),
  // An organization's override with the default it falls back to (org
  // admins; a super admin passes the tenant they administer).
  tenant: (tenantId?: string) => req<TenantRetentionView>(scoped("/v1/tenant/retention", tenantId)),
  saveTenant: (policy: RetentionPolicy, tenantId?: string) =>
    req<TenantRetentionView>(scoped("/v1/tenant/retention", tenantId), { method: "PUT", body: JSON.stringify(policy) }),
};
