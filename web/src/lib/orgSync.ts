import { req } from "./api";

// Organization sync (R11): super-admin rules mapping the directory attributes
// an auth provider asserts at sign-in onto tenant memberships, org roles and
// default rooms. Wire shapes mirror internal/orgsync.

export type OrgSyncMatchKind = "group" | "department" | "emailDomain";

export interface OrgSyncMatch {
  group?: string;
  department?: string;
  emailDomain?: string;
}

export type OrgSyncRole = "member" | "org_admin";

export interface OrgSyncRule {
  match: OrgSyncMatch;
  tenant: string;
  role: OrgSyncRole;
  defaultRooms?: string[];
}

export interface OrgSyncRuleSet {
  rules: OrgSyncRule[];
  updatedAtMs?: number;
  updatedBy?: string;
}

export interface OrgSyncChange {
  action: "add" | "role" | "remove" | "keep-manual" | "room";
  tenant: string;
  role?: string;
  fromRole?: string;
  roomId?: string;
  revokedSessions?: number;
}

export interface OrgSyncResult {
  userId: string;
  tenant?: string;
  targets: { tenant: string; role: string; defaultRooms?: string[]; rule: number }[];
  changes: OrgSyncChange[];
  dryRun?: boolean;
}

export interface OrgSyncPreviewInput {
  email: string;
  userId?: string;
  groups?: string[];
  department?: string;
}

export interface OrgSyncResyncResult {
  subjects: number;
  changed: OrgSyncResult[];
}

export interface OrgSyncDeprovisionResult {
  userId: string;
  removedTenants: string[];
  revokedSessions: number;
}

// matchKind returns the one attribute a rule matches on (rules carry exactly one).
export function matchKind(m: OrgSyncMatch): OrgSyncMatchKind {
  if (m.department) return "department";
  if (m.emailDomain) return "emailDomain";
  return "group";
}

// matchOf builds a single-key match.
export function matchOf(kind: OrgSyncMatchKind, value: string): OrgSyncMatch {
  return { [kind]: value };
}

// describeChange renders one change as a short English line.
export function describeChange(c: OrgSyncChange): string {
  switch (c.action) {
    case "add":
      return `join ${c.tenant} as ${c.role}`;
    case "role":
      return `${c.tenant}: role ${c.fromRole} → ${c.role}`;
    case "remove":
      return `leave ${c.tenant} (sync membership no longer matched)`;
    case "keep-manual":
      return `${c.tenant}: manual membership kept as ${c.role}`;
    case "room":
      return `added to room ${c.roomId} in ${c.tenant}`;
  }
}

export const orgSyncApi = {
  rules: () => req<OrgSyncRuleSet>("/v1/platform/org-sync/rules"),
  saveRules: (rules: OrgSyncRule[]) =>
    req<OrgSyncRuleSet>("/v1/platform/org-sync/rules", { method: "PUT", body: JSON.stringify({ rules }) }),
  preview: (input: OrgSyncPreviewInput) =>
    req<OrgSyncResult>("/v1/platform/org-sync/preview", { method: "POST", body: JSON.stringify(input) }),
  resync: () => req<OrgSyncResyncResult>("/v1/platform/org-sync/resync", { method: "POST", body: "{}" }),
  deprovision: (userIds: string[]) =>
    req<{ results: OrgSyncDeprovisionResult[] }>("/v1/platform/org-sync/deprovision", {
      method: "POST",
      body: JSON.stringify({ userIds }),
    }),
};
