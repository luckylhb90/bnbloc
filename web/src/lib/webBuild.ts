// The browser bundle's identity is its entry script: Vite names it
// assets/index-<content hash>.js, so a deploy that changes the app changes
// the name. The gateway serves index.html with Cache-Control: no-store, which
// makes the deployed index.html the authority on what is current.

const ENTRY = /(?:^|\/)assets\/(index-[A-Za-z0-9_-]+\.js)$/;

// entryScriptFromHtml returns the hashed entry script an index.html loads
// ("index-AbC123.js"), or null when there is none (dev server, error page).
export function entryScriptFromHtml(html: string): string | null {
  for (const m of html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)) {
    const hit = ENTRY.exec(m[1]);
    if (hit) return hit[1];
  }
  return null;
}

// runningEntryScript is the entry script this page was loaded from.
export function runningEntryScript(doc: Document): string | null {
  for (const s of Array.from(doc.querySelectorAll<HTMLScriptElement>("script[src]"))) {
    const hit = ENTRY.exec(new URL(s.src, doc.baseURI).pathname);
    if (hit) return hit[1];
  }
  return null;
}
