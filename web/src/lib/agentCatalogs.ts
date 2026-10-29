// Model catalogs for the composer's model picker
// (docs/bugs/2026-10-10-composer-model-picker-own-catalog-only.md). A catalog
// belongs to one agent, identified by (owner, core, provider): the same
// harness of two members, or under two cores, may offer different models, so
// a catalog is never matched by (core, provider) alone. Catalogs are the
// core's own data (relayed by each owner's bridge); nothing here invents one.
import type { AgentTarget } from "./apiModels.ts";
import { borrowKey, type SharedAgent } from "./roomAgents.ts";
import { sameUser } from "./format.ts";

export type { RoomAgentCatalog } from "./wire.gen.ts";
import type { RoomAgentCatalog } from "./wire.gen.ts";
import { agentIdentity, sameAgent } from "./roomAgents.ts";

// A model catalog as the picker reads it (AgentTarget's picker fields).
export interface ModelCatalog {
  readonly core?: string;
  readonly provider: string;
  readonly models?: string[];
  // The display name an agent gives a model, by id; a model with none is shown by
  // modelLabel's own rendering of its id.
  readonly modelNames?: Record<string, string>;
  readonly reasoningEfforts?: string[];
  readonly reasoningByModel?: Record<string, string[]>;
  readonly defaultModel?: string;
  readonly defaultReasoning?: string;
}

// Everything the composer resolves a room recipient's catalog from: the
// room's owner-keyed catalogs and the owner-issued borrow grants (their
// allowedModels narrow a borrowed agent's catalog).
export interface RoomModelCatalogs {
  readonly catalogs: readonly RoomAgentCatalog[];
  readonly grants: readonly SharedAgent[];
}

export const NO_ROOM_CATALOGS: RoomModelCatalogs = { catalogs: [], grants: [] };

// The recipient whose catalog is wanted (a RoomAgent's identity fields).
export interface CatalogPick {
  readonly ownerId: string;
  readonly core?: string;
  readonly provider: string;
  readonly platform?: boolean;
  // A platform agent's server-side default model (its host's policy).
  readonly defaultModel?: string;
}


// roomCatalog finds the owner's catalog for (core, provider) in the room.
export function roomCatalog(catalogs: readonly RoomAgentCatalog[], pick: CatalogPick): ModelCatalog | null {
  const row = catalogs.find((c) => sameAgent({ ownerId: c.ownerUserId, core: c.core || undefined, provider: c.provider }, { ...pick, core: pick.core || undefined }));
  if (!row) return null;
  return { core: row.core || undefined, provider: row.provider, ...(row.modelOptions ?? {}) };
}

// restrictCatalog narrows a catalog to the models an owner authorized. An
// empty list is unrestricted. The catalog's order is kept; a default the
// owner did not authorize is dropped (the picker then shows no default
// rather than a model the borrow would be refused for).
export function restrictCatalog(catalog: ModelCatalog, allowed: readonly string[] | undefined): ModelCatalog {
  if (!allowed || allowed.length === 0) return catalog;
  const ok = new Set(allowed);
  const models = (catalog.models ?? []).filter((m) => ok.has(m));
  const reasoningByModel = catalog.reasoningByModel
    ? Object.fromEntries(Object.entries(catalog.reasoningByModel).filter(([m]) => ok.has(m)))
    : undefined;
  return {
    ...catalog,
    models,
    ...(reasoningByModel ? { reasoningByModel } : {}),
    defaultModel: catalog.defaultModel && ok.has(catalog.defaultModel) ? catalog.defaultModel : undefined,
  };
}

// grantFor is the owner-issued borrow grant of the picked agent, if any
// (keyed like the authority: owner + "<core>:<provider>" or the bare
// provider).
function grantFor(grants: readonly SharedAgent[], pick: CatalogPick): SharedAgent | undefined {
  const key = borrowKey(pick);
  return grants.find((g) => sameUser(g.ownerUserId, pick.ownerId) && g.provider === key);
}

// resolveModelCatalog is the catalog the model picker shows for the composer's
// effective recipient, with send()'s precedence:
//   - no picked recipient: my selected local target (its own catalog);
//   - my own agent: the local desktop target for (core, provider) first (the
//     freshest), else the room's catalog of my agent;
//   - anyone else's agent (borrowed, platform): ONLY the room's catalog of
//     that owner's agent — never mine — narrowed to an owner-issued grant's
//     allowedModels when the owner restricted them. Platform grants carry no
//     model range; a platform agent's policy default model is the default
//     the picker shows (the bridge applies it when the send names none).
export function resolveModelCatalog(input: {
  readonly pick: CatalogPick | null;
  readonly subject: string | null;
  readonly localTargets: readonly AgentTarget[];
  readonly selectedTargetId: string;
  readonly room: RoomModelCatalogs;
}): ModelCatalog | null {
  const { pick, subject, localTargets, room } = input;
  if (!pick) return localTargets.find((t) => t.id === input.selectedTargetId) ?? null;
  if (sameUser(pick.ownerId, subject)) {
    return localTargets.find((t) => sameAgent({ ownerId: pick.ownerId, core: t.core || undefined, provider: t.provider }, { ...pick, core: pick.core || undefined })) ?? roomCatalog(room.catalogs, pick);
  }
  const catalog = roomCatalog(room.catalogs, pick);
  if (pick.platform) return platformCatalog(catalog, pick);
  if (!catalog) return null;
  return restrictCatalog(catalog, grantFor(room.grants, pick)?.allowedModels);
}

// platformCatalog overlays the platform agent's policy default on the host's
// catalog (a default the catalog does not list is still offered).
function platformCatalog(catalog: ModelCatalog | null, pick: CatalogPick): ModelCatalog | null {
  const def = pick.defaultModel;
  if (!def) return catalog;
  const base: ModelCatalog = catalog ?? { core: pick.core || undefined, provider: pick.provider };
  const models = base.models ?? [];
  return { ...base, models: models.includes(def) ? models : [...models, def], defaultModel: def };
}

// catalogRefreshKey changes whenever the room's addressable agent set does
// (roster, grants, platform agents), so the catalogs are re-read then.
export function catalogRefreshKey(agents: readonly { readonly ownerId: string; readonly core?: string; readonly provider: string }[]): string {
  return agents
    .map(agentIdentity)
    .sort()
    .join(",");
}
