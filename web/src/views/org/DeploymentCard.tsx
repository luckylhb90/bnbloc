import { useCallback, useEffect, useState } from "react";
import { WarningIcon, CheckCircleIcon } from "@phosphor-icons/react";
import { deploymentApi } from "@/lib/deploymentApi";
import { deploymentRows, shortCommit, type DeploymentVersions } from "@/lib/deployment";
import { BUILD_COMMIT } from "@/lib/buildInfo";
import { ErrorBox } from "@/ui/ErrorBox";
import { errorText } from "@/lib/errors";

// DeploymentCard (Platform console overview, super-admin): the commit every
// service instance and the embedded web bundle run, with a skew warning when
// they differ (GET /v1/platform/versions).
export function DeploymentCard() {
  const [data, setData] = useState<DeploymentVersions | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setData(await deploymentApi.versions());
    } catch (e) {
      setError(errorText(e, "failed to load deployment versions"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const staleTab = data?.web?.commit && BUILD_COMMIT && !sameCommit(data.web.commit, BUILD_COMMIT);

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Deployment</h2>
      {error && <ErrorBox>{error}</ErrorBox>}
      {data && (
        <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-sm">
          {data.consistent && (data.warnings ?? []).length > 0 ? (
            <div className="flex flex-col gap-1 text-[var(--color-warning,#b45309)]">
              <div className="flex items-center gap-2 font-medium">
                <WarningIcon size={16} /> Versions can't be verified (images built without CI_COMMIT_SHA)
              </div>
              {(data.warnings ?? []).map((w, i) => (
                <div key={i} className="text-xs">
                  {w}
                </div>
              ))}
            </div>
          ) : data.consistent ? (
            <div className="flex items-center gap-2 text-[var(--color-fg-muted)]">
              <CheckCircleIcon size={16} /> Every component runs {shortCommit(data.services[0]?.commit ?? "")}
            </div>
          ) : (
            <div className="flex flex-col gap-1 text-[var(--color-danger,#b91c1c)]">
              <div className="flex items-center gap-2 font-medium">
                <WarningIcon size={16} /> Version skew
              </div>
              {(data.problems ?? []).map((p, i) => (
                <div key={i} className="text-xs">
                  {p}
                </div>
              ))}
            </div>
          )}
          <table className="w-full text-xs">
            <thead className="text-left uppercase tracking-wide text-[var(--color-fg-subtle)]">
              <tr>
                <th className="py-1 pr-3">Service</th>
                <th className="py-1 pr-3">Commit</th>
                <th className="py-1">Instances</th>
              </tr>
            </thead>
            <tbody className="font-mono">
              {deploymentRows(data).map((r) => (
                <tr key={r.service} className="border-t border-[var(--color-border)]">
                  <td className="py-1 pr-3 font-sans">{r.service}</td>
                  <td className={r.commits.length > 1 ? "py-1 pr-3 text-[var(--color-danger,#b91c1c)]" : "py-1 pr-3"}>
                    {r.commits.join(", ")}
                  </td>
                  <td className="py-1 text-[var(--color-fg-muted)]">{r.instances.join(", ")}</td>
                </tr>
              ))}
              {data.web && (
                <tr className="border-t border-[var(--color-border)]">
                  <td className="py-1 pr-3 font-sans">web bundle</td>
                  <td className="py-1 pr-3">{shortCommit(data.web.commit)}</td>
                  <td className="py-1 text-[var(--color-fg-muted)]">v{data.web.version}</td>
                </tr>
              )}
              {(data.unreachable ?? []).map((u) => (
                <tr key={u.service} className="border-t border-[var(--color-border)]">
                  <td className="py-1 pr-3 font-sans">{u.service.replace(/^bnbloc-/, "")}</td>
                  <td className="py-1 pr-3 text-[var(--color-danger,#b91c1c)]">unreachable</td>
                  <td className="py-1 text-[var(--color-fg-muted)]">{u.error}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {staleTab && (
            <div className="text-xs text-[var(--color-fg-muted)]">
              This page runs {shortCommit(BUILD_COMMIT)}; reload to pick up the deployed web bundle.
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function sameCommit(a: string, b: string): boolean {
  return a.startsWith(b) || b.startsWith(a);
}
