import { useCallback, useEffect, useState } from "react";
import { CheckIcon, FileCodeIcon, XIcon } from "@phosphor-icons/react";
import { contentLabel, permissionLabel, type ContentComponent, type ContentPublishRequest } from "@/lib/contentComponents";
import { useContentChanged } from "@/lib/useContentChanged";
import { ContentKindValues } from "@/lib/wire.gen";
import { fileChangeLabel, requestTitle, type ContentAdminComponent, type ContentReview } from "@/lib/contentSharing";
import { contentSharingApi } from "@/lib/contentSharingApi";
import { relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Textarea } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { PermissionBadges } from "@/views/library/ContentViewDialog";
import { TeamRoster } from "@/views/library/ContentFormExtras";
import { UserName } from "@/views/UserName";
import { LineDiff } from "@/views/workbench/VersionDiff";
import { errorText } from "@/lib/errors";

// Org-admin: content members asked to publish to the organization
// (docs/prd/2026-10-12-experts-and-teams.md, step 4). Each request is
// reviewed (declared permissions, the files that run, MCP tools and other
// requirements, a team's roster, and what changed since the version the
// organization uses) and approved or declined with a note for its author.
// Below, what is published: unpublish (hidden from the Library and new use;
// rooms that pinned it keep their version), restore or remove it.
export function ContentReviewTab() {
  const toast = useToast();
  const [requests, setRequests] = useState<readonly ContentPublishRequest[] | null>(null);
  const [published, setPublished] = useState<readonly ContentAdminComponent[] | null>(null);
  const [history, setHistory] = useState(false);
  const [reviewing, setReviewing] = useState<string | null>(null);
  const [removing, setRemoving] = useState<ContentComponent | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [r, p] = await Promise.all([contentSharingApi.requests(history), contentSharingApi.orgContent()]);
      setRequests(r.requests ?? []);
      setPublished(p.components ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load content requests"));
      setRequests([]);
      setPublished([]);
    }
  }, [history]);

  useEffect(() => {
    void load();
  }, [load]);
  // Any content change (a decision, an unpublish, a removal here; a publish
  // request or withdrawal by an author in this page) reads the lists again.
  useContentChanged(ContentKindValues, () => void load());

  const setStatus = async (c: ContentComponent, action: "unpublish" | "restore") => {
    try {
      await contentSharingApi.setOrgStatus(c.id, action);
      toast.success(action === "unpublish" ? `${contentLabel(c)} is unpublished` : `${contentLabel(c)} is published again`);
    } catch (e) {
      toast.error(errorText(e, "Could not change it"));
    }
  };
  const remove = async (c: ContentComponent) => {
    try {
      await contentSharingApi.removeOrg(c.id);
      toast.success(`${contentLabel(c)} is removed from your organization`);
    } catch (e) {
      toast.error(errorText(e, "Could not remove it"));
    }
  };

  const pending = (requests ?? []).filter((r) => r.status === "pending");
  const decided = (requests ?? []).filter((r) => r.status !== "pending");
  return (
    <section className="flex flex-col gap-6 p-6">
      <div className="flex flex-col gap-3">
        <div className="flex items-start gap-3">
          <div className="flex-1">
            <h2 className="text-sm font-semibold">Waiting for approval</h2>
            <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
              Experts, skill packs and prompt packs members asked to publish to everyone in the organization. Members keep the approved version until you approve a newer one.
            </p>
          </div>
          <Switch checked={history} onChange={setHistory} label="Show decided" />
        </div>
        {error && <ErrorBox>{error}</ErrorBox>}
        {requests && pending.length === 0 && !error && <p className="text-sm text-[var(--color-fg-subtle)]">Nothing waits for approval.</p>}
        {pending.map((r) => (
          <RequestRow key={r.id} request={r} onReview={() => setReviewing(r.id)} />
        ))}
        {history && decided.length > 0 && (
          <div className="flex flex-col gap-2">
            <h3 className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Decided</h3>
            {decided.map((r) => (
              <RequestRow key={r.id} request={r} />
            ))}
          </div>
        )}
      </div>

      <div className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-semibold">Published in your organization</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
            Unpublishing hides it from the Library and new use (rooms that pinned it keep their version); removing deletes it here. Its author keeps their own.
          </p>
        </div>
        {published && published.length === 0 && !error && <p className="text-sm text-[var(--color-fg-subtle)]">Nothing is published yet.</p>}
        {(published ?? []).map(({ component: c }) => (
          <Card key={c.id} className="flex flex-wrap items-center gap-3 p-3">
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">
                {contentLabel(c)} <span className="font-mono text-xs text-[var(--color-fg-subtle)]">v{c.latestVersion}</span>
              </p>
              <p className="truncate text-xs text-[var(--color-fg-subtle)]">
                {c.kind} · by <UserName id={c.owner} /> · {relativeTime(c.updatedAtMs)}
              </p>
            </div>
            <PermissionBadges component={c} />
            {c.status === "retired" ? <Badge>Unpublished</Badge> : <Badge tone="accent">Published</Badge>}
            {c.status === "retired" ? (
              <Button size="sm" variant="outline" onClick={() => void setStatus(c, "restore")}>
                Restore
              </Button>
            ) : (
              <Button size="sm" variant="outline" onClick={() => void setStatus(c, "unpublish")}>
                Unpublish
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => setRemoving(c)}>
              Remove
            </Button>
          </Card>
        ))}
      </div>

      <ReviewDialog
        requestId={reviewing}
        onClose={() => setReviewing(null)}
        onDecided={() => setReviewing(null)}
      />
      <ConfirmDialog
        open={!!removing}
        onClose={() => setRemoving(null)}
        onConfirm={() => removing && void remove(removing)}
        title="Remove from your organization?"
        description={removing ? `${contentLabel(removing)} is deleted for the organization, and rooms using it lose it. Its author keeps their own and can ask again.` : undefined}
        confirmLabel="Remove"
        danger
      />
    </section>
  );
}

function RequestRow({ request: r, onReview }: { readonly request: ContentPublishRequest; readonly onReview?: () => void }) {
  return (
    <Card className="flex flex-wrap items-center gap-3 p-3">
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{requestTitle(r)}</p>
        <p className="truncate text-xs text-[var(--color-fg-subtle)]">
          {r.kind} · asked by <UserName id={r.requestedBy} /> · {relativeTime(r.createdAtMs)}
          {r.approvedVersion ? ` · the organization uses ${r.approvedVersion}` : " · new to the organization"}
        </p>
        {r.note && <p className="mt-1 line-clamp-2 text-sm text-[var(--color-fg-muted)]">“{r.note}”</p>}
        {r.status !== "pending" && (
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
            {r.status === "approved" ? "Approved" : r.status === "declined" ? "Declined" : "Withdrawn"}
            {r.decidedBy && (
              <>
                {" by "}
                <UserName id={r.decidedBy} />
              </>
            )}
            {r.decidedAtMs ? ` ${relativeTime(r.decidedAtMs)}` : ""}
            {r.decisionNote ? `: ${r.decisionNote}` : ""}
          </p>
        )}
      </div>
      {onReview && (
        <Button size="sm" variant="primary" onClick={onReview}>
          Review
        </Button>
      )}
    </Card>
  );
}

// ReviewDialog shows what an approver decides on and takes the decision.
function ReviewDialog({
  requestId,
  onClose,
  onDecided,
}: {
  readonly requestId: string | null;
  readonly onClose: () => void;
  readonly onDecided: () => void;
}) {
  const toast = useToast();
  const [review, setReview] = useState<ContentReview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!requestId) return;
    let live = true;
    setReview(null);
    setError(null);
    setNote("");
    contentSharingApi
      .review(requestId)
      .then((r) => live && setReview(r))
      .catch((e) => live && setError(errorText(e, "Could not load the request")));
    return () => {
      live = false;
    };
  }, [requestId]);

  const decide = async (decision: "approve" | "decline") => {
    if (!requestId) return;
    setBusy(true);
    try {
      await contentSharingApi.decide(requestId, decision, note.trim());
      toast.success(decision === "approve" ? "Approved: it is published to your organization" : "Declined: its author is told why");
      onDecided();
    } catch (e) {
      toast.error(errorText(e, decision === "approve" ? "Could not approve it" : "Could not decline it"));
    } finally {
      setBusy(false);
    }
  };

  const c = review?.component;
  const prev = review?.previous;
  return (
    <Dialog
      open={!!requestId}
      onClose={onClose}
      size="lg"
      title={review ? `Review ${requestTitle(review.request)}` : "Review"}
      description={
        review
          ? prev
            ? `A new version for your organization, which uses version ${prev.version} now. Approving it moves everyone to this version.`
            : "New to your organization. Approving it lists it in every member's Library."
          : undefined
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="outline" onClick={() => void decide("decline")} disabled={!review || busy}>
            <XIcon size={14} />
            Decline
          </Button>
          <Button variant="primary" onClick={() => void decide("approve")} disabled={!review || busy}>
            <CheckIcon size={14} />
            Approve
          </Button>
        </>
      }
    >
      {error && <ErrorBox>{error}</ErrorBox>}
      {!review && !error && <p className="text-sm text-[var(--color-fg-subtle)]">Loading…</p>}
      {review && c && (
        <div className="flex flex-col gap-5 text-sm">
          <div className="flex flex-col gap-1">
            <p className="text-[var(--color-fg-muted)]">
              {c.kind} · asked by <UserName id={review.request.requestedBy} /> · {relativeTime(review.request.createdAtMs)}
            </p>
            {c.description && <p>{c.description}</p>}
            {review.request.note && <p className="text-[var(--color-fg-muted)]">“{review.request.note}”</p>}
          </div>

          <ReviewSection title="Declared permissions">
            {c.permissions.length === 0 ? (
              <p className="text-[var(--color-fg-subtle)]">None: it runs no scripts and no local tools.</p>
            ) : (
              <>
                <PermissionBadges component={c} />
                <p className="text-xs text-[var(--color-fg-subtle)]">
                  Other members' runs of it are sandboxed ({c.permissions.map(permissionLabel).join(", ").toLowerCase()}).
                </p>
              </>
            )}
            {prev && prev.permissions.join(",") !== c.permissions.join(",") && (
              <p className="text-xs text-[var(--color-warning)]">Changed from: {prev.permissions.length ? prev.permissions.map(permissionLabel).join(", ") : "none"}</p>
            )}
          </ReviewSection>

          {review.scriptFiles.length > 0 && (
            <ReviewSection title="Files that run">
              <ul className="flex flex-col gap-0.5 font-mono text-xs">
                {review.scriptFiles.map((f) => (
                  <li key={f} className="flex items-center gap-1.5">
                    <FileCodeIcon size={13} /> {f}
                  </li>
                ))}
              </ul>
            </ReviewSection>
          )}

          {((c.requires.mcp?.length ?? 0) > 0 || (c.requires.skills?.length ?? 0) > 0 || (c.requires.prompts?.length ?? 0) > 0) && (
            <ReviewSection title="Requirements">
              <ul className="flex flex-col gap-0.5 text-xs">
                {c.requires.mcp?.map((m) => (
                  <li key={`mcp:${m.name}`}>
                    MCP tool <span className="font-mono">{m.name}</span>
                    {m.settings && m.settings.length > 0 ? ` (needs ${m.settings.join(", ")})` : ""}
                  </li>
                ))}
                {c.requires.skills?.map((s) => (
                  <li key={`skill:${s}`}>
                    Skill pack <span className="font-mono">{s}</span>
                  </li>
                ))}
                {c.requires.prompts?.map((p) => (
                  <li key={`prompt:${p}`}>
                    Prompt pack <span className="font-mono">{p}</span>
                  </li>
                ))}
              </ul>
            </ReviewSection>
          )}

          {c.team && (
            <ReviewSection title="Team roster">
              <TeamRoster team={c.team} />
            </ReviewSection>
          )}

          {(c.skills?.length ?? 0) > 0 && (
            <ReviewSection title="Skills">
              <ul className="flex flex-col gap-0.5 text-xs">
                {c.skills?.map((s) => (
                  <li key={s.name}>
                    <span className="font-mono">{s.name}</span>: {s.description}
                  </li>
                ))}
              </ul>
            </ReviewSection>
          )}

          <ReviewSection title={prev ? `Changes since version ${prev.version}` : "Files"}>
            {review.files.length === 0 ? (
              <p className="text-[var(--color-fg-subtle)]">No file changed.</p>
            ) : (
              review.files.map((f) => (
                <details key={f.path} className="rounded-[var(--radius)] border border-[var(--color-border)]" open={review.files.length <= 3}>
                  <summary className="cursor-pointer px-3 py-1.5 text-xs">
                    <span className="font-mono">{f.path}</span> <Badge>{fileChangeLabel(f)}</Badge>
                    {f.truncated && <span className="ml-1 text-[var(--color-fg-subtle)]">(shortened)</span>}
                  </summary>
                  {f.binary ? (
                    <p className="px-3 pb-2 text-xs text-[var(--color-fg-subtle)]">Binary file.</p>
                  ) : (
                    <div className="max-h-72 overflow-auto border-t border-[var(--color-border)]">
                      <LineDiff prev={f.before ?? ""} curr={f.after ?? ""} />
                    </div>
                  )}
                </details>
              ))
            )}
          </ReviewSection>

          <Field label="Note for the author (optional)" hint="Sent with your decision.">
            <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} maxLength={500} />
          </Field>
        </div>
      )}
    </Dialog>
  );
}

function ReviewSection({ title, children }: { readonly title: string; readonly children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <h3 className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">{title}</h3>
      {children}
    </div>
  );
}
