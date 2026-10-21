import { req } from "./api.ts";
import type { ContentComponentDetail } from "./contentComponents.ts";
import { changing } from "./contentChanges.ts";
import { noteSaved } from "./contentComponentsApi.ts";
import { promptImportContentType, promptPackPayload, type PromptPackForm } from "./promptPacks.ts";

export type { SavePromptReply } from "./wire.gen.ts";
import type { SavePromptReply } from "./wire.gen.ts";

const enc = encodeURIComponent;

// Prompt packs over the content routes (services/gateway/content_prompts.go
// and content_components.go). Listing, uses, versions and delete are the
// generic contentApi calls. Every save announces a prompt change
// (lib/contentChanges.ts), so open composers and the Library read again.
export const promptPacksApi = {
  create: (form: PromptPackForm) =>
    changing(req<ContentComponentDetail>("/v1/content-components", { method: "POST", body: JSON.stringify({ form: promptPackPayload(form) }) }), "prompt"),
  newVersion: (id: string, form: PromptPackForm, baseVersion: string) =>
    changing(
      req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, {
        method: "POST",
        body: JSON.stringify({ form: promptPackPayload(form), baseVersion }),
      }).then(noteSaved),
      "prompt",
    ),
  // Import a markdown or json file of prompts (or an archive of them) as a
  // new pack, or (existingId) as a new version of one of the caller's.
  importFile: (file: File, opts: { name?: string; existingId?: string }): Promise<ContentComponentDetail> => {
    const q = new URLSearchParams();
    if (opts.existingId) q.set("id", opts.existingId);
    else {
      q.set("kind", "prompt");
      if (opts.name?.trim()) q.set("name", opts.name.trim());
    }
    return changing(
      req<ContentComponentDetail>(`/v1/content-components/import?${q}`, {
        method: "POST",
        headers: { "Content-Type": promptImportContentType(file.name) },
        body: file,
      }).then(noteSaved),
      "prompt",
    );
  },
  // "Save as prompt": into the caller's personal pack (created on the
  // first save, as a new version after).
  savePrompt: (body: { title?: string; body: string; tags?: readonly string[] }) =>
    changing(req<SavePromptReply>("/v1/content-components/prompts", { method: "POST", body: JSON.stringify(body) }), "prompt"),
  // Count one insertion of a prompt (best-effort; never blocks inserting).
  recordUse: (componentId: string, promptId: string, opts: { roomId?: string; version?: string } = {}) => {
    const q = new URLSearchParams();
    if (opts.roomId) q.set("room", opts.roomId);
    if (opts.version) q.set("version", opts.version);
    const qs = q.toString();
    return req<void>(`/v1/content-components/${enc(componentId)}/prompts/${enc(promptId)}/use${qs ? `?${qs}` : ""}`, { method: "POST", as: "none" });
  },
};
