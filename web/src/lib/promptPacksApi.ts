import { ApiError, req } from "./api.ts";
import { apiUrl } from "./config.ts";
import { authedFetch, errorDetail } from "./session.ts";
import type { ContentComponent, ContentComponentDetail } from "./contentComponents.ts";
import { promptImportContentType, promptPackPayload, type PromptPackForm } from "./promptPacks.ts";

const enc = encodeURIComponent;

// PROMPT_PACKS_CHANGED is announced after a save changes the member's
// prompt packs (Save as prompt, the Library), so open composers reload.
export const PROMPT_PACKS_CHANGED = "bnbloc:prompt-packs-changed";

export function announcePromptPacksChanged(): void {
  window.dispatchEvent(new Event(PROMPT_PACKS_CHANGED));
}

export interface SavePromptReply {
  readonly component: ContentComponent;
  readonly promptId: string;
  readonly created: boolean;
}

// Prompt packs over the content routes (services/gateway/content_prompts.go
// and content_components.go). Listing, uses, versions and delete are the
// generic contentApi calls.
export const promptPacksApi = {
  create: (form: PromptPackForm) =>
    req<ContentComponentDetail>("/v1/content-components", { method: "POST", body: JSON.stringify({ form: promptPackPayload(form) }) }),
  newVersion: (id: string, form: PromptPackForm, baseVersion: string) =>
    req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, {
      method: "POST",
      body: JSON.stringify({ form: promptPackPayload(form), baseVersion }),
    }),
  // Import a markdown or json file of prompts (or an archive of them) as a
  // new pack, or (existingId) as a new version of one of the caller's.
  importFile: async (file: File, opts: { name?: string; existingId?: string }): Promise<ContentComponentDetail> => {
    const q = new URLSearchParams();
    if (opts.existingId) q.set("id", opts.existingId);
    else {
      q.set("kind", "prompt");
      if (opts.name?.trim()) q.set("name", opts.name.trim());
    }
    const res = await authedFetch(apiUrl(`/v1/content-components/import?${q}`), {
      method: "POST",
      headers: { "Content-Type": promptImportContentType(file.name) },
      body: file,
    });
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return (await res.json()) as ContentComponentDetail;
  },
  // "Save as prompt": into the caller's personal pack (created on the
  // first save, as a new version after).
  savePrompt: (body: { title?: string; body: string; tags?: readonly string[] }) =>
    req<SavePromptReply>("/v1/content-components/prompts", { method: "POST", body: JSON.stringify(body) }),
  // Count one insertion of a prompt (best-effort; never blocks inserting).
  recordUse: (componentId: string, promptId: string, opts: { roomId?: string; version?: string } = {}) => {
    const q = new URLSearchParams();
    if (opts.roomId) q.set("room", opts.roomId);
    if (opts.version) q.set("version", opts.version);
    const qs = q.toString();
    return req<void>(`/v1/content-components/${enc(componentId)}/prompts/${enc(promptId)}/use${qs ? `?${qs}` : ""}`, { method: "POST", raw: true });
  },
};
