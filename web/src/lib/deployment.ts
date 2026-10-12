// Deployment build identity (GET /v1/platform/versions, super-admin): wire
// shapes are internal/wire/versions.go's (wire.gen.ts).

export type { DeploymentVersions } from "./wire.gen.ts";
import type { DeploymentVersions } from "./wire.gen.ts";

export interface DeploymentRow {
  service: string;
  instances: string[];
  commits: string[];
}

// shortCommit trims a hash for display.
export function shortCommit(commit: string): string {
  return commit ? commit.slice(0, 12) : "unknown";
}

// deploymentRows groups instances per service (one row each, in the server's
// order) with the distinct commits they run, so a skewed replica shows as a
// second commit on its service's row.
export function deploymentRows(d: DeploymentVersions): DeploymentRow[] {
  const rows = new Map<string, DeploymentRow>();
  for (const s of d.services) {
    let row = rows.get(s.service);
    if (!row) {
      row = { service: s.service.replace(/^bnbloc-/, ""), instances: [], commits: [] };
      rows.set(s.service, row);
    }
    row.instances.push(s.instance);
    const c = shortCommit(s.commit);
    if (!row.commits.includes(c)) row.commits.push(c);
  }
  return [...rows.values()];
}
