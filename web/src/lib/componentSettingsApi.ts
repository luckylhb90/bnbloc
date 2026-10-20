import { componentPath, req, type ComponentRef } from "./api.ts";
import type { ComponentSettingsUpdate, ComponentSettingsView } from "./componentSettings.ts";
import type { OrgSettingsComponentsReply } from "./wire.gen.ts";

// The settings of an installed component (services/gateway, docs/prd/
// 2026-10-19-component-settings.md): the platform scope (super admins) and
// the organization scope (org admins, in the tenant the session acts in: a
// super admin's acting tenant, lib/session.ts). A save answers the view as
// stored; a value the declaration refuses is a 400 with its reason.
export type SettingsOwner = "platform" | "org";

const base: Record<SettingsOwner, string> = {
  platform: "/v1/platform/components",
  org: "/v1/tenant/components",
};

export const componentSettingsApi = {
  get: (owner: SettingsOwner, component: ComponentRef) => req<ComponentSettingsView>(componentPath(base[owner], component, "/settings")),
  save: (owner: SettingsOwner, component: ComponentRef, update: ComponentSettingsUpdate) =>
    req<ComponentSettingsView>(componentPath(base[owner], component, "/settings"), { method: "PUT", body: JSON.stringify(update) }),
  // The components of any kind whose organization settings the acting
  // organization sets (an endpoint component beside the gateway is in no
  // member's catalog).
  orgComponents: () => req<OrgSettingsComponentsReply>("/v1/tenant/component-settings"),
};
