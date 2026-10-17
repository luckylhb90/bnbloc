// Decision models (docs/prd/2026-10-17-decision-models.md): the one owner of
// the decision routes in the web. Today that is the consoles' provider API;
// the member route (POST /v1/decisions) joins here with its first web
// caller. No other file names a decision route.

import { req } from "./api.ts";
import type { MediaProviderApi, MediaProviderConfigView, MediaProviderUpdateBody } from "./media.ts";

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
