import { useCallback, useEffect, useState } from "react";

// Hash-based routing for the app shell. It is deliberately hash-based, not
// path-based: the SPA ships with Vite base:'./' (relative assets) and runs
// from a tauri:// bundle with no server SPA fallback, so a real path segment
// would break asset resolution on reload. A hash keeps the document path at
// root in every form. See docs/architecture/web-client-multi-form.md.
// The route shapes, including a room's message anchor, live in hashRoute.ts.

import { parseHashRoute as parse, routeHash, type AppRoute } from "./hashRoute.ts";

export { routeHash, type AppRoute };

// useHashRoute returns the current route and a navigate function. It ignores a
// preview hash (owned by StandalonePreview) and a key=value SSO hash.
export function useHashRoute(): [AppRoute, (route: AppRoute) => void] {
  const [route, setRoute] = useState<AppRoute>(() => parse(window.location.hash));

  useEffect(() => {
    const onHash = () => {
      // Preview windows own their hash (StandalonePreview parses it). Still
      // re-render so the App re-evaluates its preview gate, but do not map a
      // preview hash onto an app route.
      setRoute(parse(window.location.hash));
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // Stable across renders: consumers put it (and callbacks built on it) in
  // effect dependencies.
  const navigate = useCallback((next: AppRoute) => {
    const hash = routeHash(next);
    if (window.location.hash !== hash) {
      window.location.hash = hash; // fires hashchange -> setRoute
    } else {
      setRoute(next);
    }
  }, []);

  return [route, navigate];
}
