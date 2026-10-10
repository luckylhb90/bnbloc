import { req } from "./api.ts";
import type { Profile } from "./displayNames.ts";

// Display-name endpoints (PRD 2026-10-10 §1). The id stays the identity; these
// only ever carry the display name.
export const profilesApi = {
  mine: () => req<Profile>("/v1/my/profile"),
  // Empty clears the name (the default rule applies again).
  save: (displayName: string) =>
    req<Profile>("/v1/my/profile", { method: "PUT", body: JSON.stringify({ displayName }) }),
  // At most 200 ids; the server applies the default rule to unset names.
  lookup: async (ids: readonly string[]): Promise<readonly Profile[]> => {
    const q = new URLSearchParams({ ids: ids.join(",") });
    const reply = await req<{ profiles?: Profile[] | null }>(`/v1/users/profiles?${q.toString()}`);
    return reply?.profiles ?? [];
  },
};
