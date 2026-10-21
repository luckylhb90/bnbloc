import { useEffect, useState } from "react";
import { ShareFatIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { shareTargetsApi } from "@/lib/shareTargetsApi";
import type { ShareItem, ShareTargetInfo } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";

// One read of the organization's share targets per sign-in and organization
// (the list changes when an admin installs or scopes one; a reload shows it).
const lists = new Map<string, Promise<readonly ShareTargetInfo[]>>();

function targetsFor(key: string): Promise<readonly ShareTargetInfo[]> {
  let list = lists.get(key);
  if (!list) {
    list = shareTargetsApi.list().then(
      (r) => r.targets,
      () => {
        lists.delete(key); // a failed read is tried again next time
        return [] as readonly ShareTargetInfo[];
      },
    );
    lists.set(key, list);
  }
  return list;
}

/** useShareTargets is the share targets the signed-in member's organization
 *  has: empty while loading, when there is none, and when the read fails. */
export function useShareTargets(): readonly ShareTargetInfo[] {
  const { subject, tenant } = useAuth();
  const [targets, setTargets] = useState<readonly ShareTargetInfo[]>([]);
  useEffect(() => {
    if (!subject) return;
    let live = true;
    void targetsFor(`${subject}|${tenant}`).then((t) => live && setTargets(t));
    return () => {
      live = false;
    };
  }, [subject, tenant]);
  return targets;
}

/** ShareToTargetButton shares item through each installed share target: one
 *  button per target, none when there is no target. A click starts the share
 *  and navigates to its page, which the other app's client runs. */
export function ShareToTargetButton({ item, className }: { item: ShareItem; className: string }) {
  const targets = useShareTargets();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  if (targets.length === 0) return null;
  const go = async (name: string) => {
    setBusy(true);
    try {
      window.location.assign((await shareTargetsApi.start(name, item)).url);
    } catch (e) {
      toast.error(errorText(e, "Could not start sharing"));
      setBusy(false);
    }
  };
  return (
    <>
      {targets.map((t) => (
        <button
          key={t.name}
          type="button"
          className={className}
          disabled={busy}
          onClick={() => void go(t.name)}
          title={`Share via ${t.name}`}
          aria-label={`Share this via ${t.name}`}
        >
          <ShareFatIcon size={16} />
        </button>
      ))}
    </>
  );
}
