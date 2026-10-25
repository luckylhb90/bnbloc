// Decision models (docs/prd/2026-10-17-decision-models.md): the one owner of
// the decision routes in the web: the consoles' provider API and the member
// route (POST /v1/decisions), whose first caller is the composer's suggestions
// (lib/suggestUse.ts). No other file names a decision route.

import { req } from "./api.ts";
import type { MediaProviderApi, MediaProviderConfigView, MediaProviderUpdateBody } from "./media.ts";
import type { DecisionReply, DecisionRequest } from "./wire.gen.ts";

/** The member's decision calls: whether the organization's decision model runs
 *  for them, and one call (a signal drops an answer nobody waits for). */
export const decisionsApi = {
  available: () => req<{ available: boolean; models?: string[] }>("/v1/decisions"),
  decide: (body: DecisionRequest, signal?: AbortSignal) =>
    req<DecisionReply>("/v1/decisions", { method: "POST", body: JSON.stringify(body), signal }),
};

/** The adapter kinds the console offers for a decision model. */
export const DECISION_ADAPTER_KINDS = ["openai-decisions"] as const;

/** The console's decision provider API: the platform's (super admin) and
 *  the organization's own (org admin). The provider uses the media
 *  provider's shapes: one console form. */
export function decisionProviderApi(scope: "platform" | "org" = "platform"): MediaProviderApi {
  const base = scope === "org" ? "/v1/tenant/decisions" : "/v1/platform/decisions";
  return {
    get: () => req<MediaProviderConfigView>(base),
    put: (body: MediaProviderUpdateBody) => req<MediaProviderConfigView>(base, { method: "PUT", body: JSON.stringify(body) }),
    test: () => req<{ ok: boolean; error?: string; reply?: string; latencyMs: number }>(`${base}/test`, { method: "POST" }),
  };
}
