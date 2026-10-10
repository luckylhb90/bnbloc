import { useEffect, useSyncExternalStore } from "react";
import { NameResolver } from "./displayNames.ts";
import { profilesApi } from "./profilesApi.ts";

// The app's one display-name cache. Every person rendering goes through it
// (via UserName / AgentHandle or these hooks), so a page full of message rows
// costs one batched lookup, not one per row.
export const nameResolver = new NameResolver({ fetchProfiles: profilesApi.lookup });

// displayNameOf is the imperative read for strings built outside render
// (toasts, aria labels built in callbacks): the cached name or the default
// rule, and it queues a lookup so the next render has the real name.
export function displayNameOf(id: string): string {
  nameResolver.request([id]);
  return nameResolver.peek(id);
}

function useResolverVersion(): number {
  return useSyncExternalStore(nameResolver.subscribe, nameResolver.getVersion, nameResolver.getVersion);
}

export function useDisplayName(id: string | null | undefined): string {
  useResolverVersion();
  useEffect(() => {
    if (id) nameResolver.request([id]);
  }, [id]);
  return id ? nameResolver.peek(id) : "";
}

// useDisplayNames resolves a list at once and returns a lookup function.
export function useDisplayNames(ids: readonly (string | null | undefined)[]): (id: string) => string {
  useResolverVersion();
  const key = ids.filter(Boolean).join("\n");
  useEffect(() => {
    if (key) nameResolver.request(key.split("\n"));
  }, [key]);
  return (id: string) => {
    if (!id) return "";
    nameResolver.request([id]);
    return nameResolver.peek(id);
  };
}
