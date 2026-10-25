// The Library's candidate shortlist (internal/wire/library_candidates.go): the server finds
// the experts, skill packs and tools that may fit a request the member sent, so the client
// holds no catalog and cost does not grow with the Library
// (docs/prd/2026-10-25-suggest-what-to-use.md). No other file names the route.
import { req } from "./api.ts";
import type { LibraryCandidates } from "./wire.gen.ts";

export const libraryApi = {
  /** candidates is the shortlist for a request, best first, at most limit (the server's default
   *  when omitted). The request is the query: the server does not store it. */
  candidates: (request: string, limit?: number, signal?: AbortSignal) => {
    const q = new URLSearchParams({ q: request });
    if (limit) q.set("limit", String(limit));
    return req<LibraryCandidates>(`/v1/library/candidates?${q}`, { signal });
  },
};
