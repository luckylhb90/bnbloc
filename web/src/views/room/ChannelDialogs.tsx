import { useEffect, useMemo, useState } from "react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { DRAFT_CHANNEL_PURPOSE, AI_UNAVAILABLE, purposeAsk, purposeOf } from "@/lib/channelAssists";
import { mayPlaceChannel, sectionOf } from "@/lib/channelRules";
import { isForum, linesOf, statusesFromLines, tagsFromLines } from "@/lib/forum";
import { errorText } from "@/lib/errors";
import { agentByIdentity, agentHandle, agentIdentity } from "@/lib/roomAgents";
import type { RoomMembership } from "@/lib/roomRoles";
import { displayNameOf } from "@/lib/useDisplayName";
import {
  ChannelAgentParticipationValues,
  MaxChannelGroupName,
  MaxChannelPurpose,
  MaxChannelRules,
  MaxPostTemplate,
  type ChannelAgentParticipation,
  type ChannelPreset,
  type ChannelCreationPolicy,
  type ChannelGroup,
  type ChannelSettings,
  type ChannelWhoPosts,
  type RoomSection,
  type SectionJoinPolicy,
  type SectionJoinRequest,
} from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { AssistButton } from "@/views/AssistButton";
import type { RoomAgent } from "@/views/composer/types";
import { useChannelAssist } from "@/views/room/useChannelAssist";
import { useApplyPreset } from "@/views/room/ChannelPresetDialogs";
import { ChannelPresetPicker } from "@/views/room/ChannelPresetPicker";

// The channel and section dialogs of the rail (docs/prd/2026-10-28-channels-
// and-threads.md §2.1, §2.1a, §8 slice 1). Each says a refusal inside
// itself, in the server's words (the control that acted), and offers only
// the places and fields the member may set (lib/channelRules).

const PARTICIPATION: Record<ChannelAgentParticipation, string> = {
  "": "When mentioned",
  questions: "Every question",
  threads: "Every new thread",
};

/** The places a member may put a channel: the top level and the sections
 *  the room's creation policy lets them use. */
function placeOptions(me: RoomMembership | null, subject: string | null, policy: ChannelCreationPolicy | "", sections: readonly RoomSection[], keep?: string) {
  const out: { readonly value: string; readonly label: string }[] = [];
  if ((me && mayPlaceChannel(me, subject, policy, null)) || keep === "") out.push({ value: "", label: "Top level" });
  for (const s of sections) if ((me && mayPlaceChannel(me, subject, policy, s)) || keep === s.sectionId) out.push({ value: s.sectionId, label: s.name });
  return out;
}

export function CreateChannelDialog({
  roomId,
  subject,
  me,
  policy,
  sections,
  onClose,
  onCreated,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly me: RoomMembership | null;
  readonly policy: ChannelCreationPolicy | "";
  readonly sections: readonly RoomSection[];
  readonly onClose: () => void;
  readonly onCreated: (c: Channel) => void;
}) {
  const places = placeOptions(me, subject, policy, sections);
  const [name, setName] = useState("");
  const [purpose, setPurpose] = useState("");
  const [sectionId, setSectionId] = useState(places[0]?.value ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  // A channel made from a preset (§2.1c): created, then the preset applied by
  // the one apply (useApplyPreset), so Undo is the same as anywhere.
  const [fromPreset, setFromPreset] = useState(false);
  const [preset, setPreset] = useState<{ readonly label: string; readonly channel: ChannelPreset } | null>(null);
  const applyPreset = useApplyPreset(roomId, noop);
  const create = async () => {
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr("");
    try {
      const made = await channelsApi.create(roomId, { name: name.trim(), purpose: purpose.trim(), sectionId });
      if (fromPreset && preset) {
        try {
          await applyPreset(made, preset);
        } catch (e) {
          setErr(errorText(e, `#${made.name} was made, but the preset was not applied`));
        }
      }
      onCreated(made);
    } catch (e) {
      setErr(errorText(e, "Could not create the channel"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title="New channel"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void create()} disabled={busy || !name.trim() || places.length === 0}>
            {busy ? "Creating…" : "Create channel"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Name">
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="releases" onKeyDown={(e) => e.key === "Enter" && void create()} />
        </Field>
        <Field label="Purpose" hint="One line shown under its name; agents read it too.">
          <Input value={purpose} maxLength={MaxChannelPurpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What this channel is for" />
        </Field>
        {places.length > 1 && (
          <Field label="Where">
            <Select value={sectionId} onChange={(e) => setSectionId(e.target.value)} aria-label="Section">
              {places.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {places.length === 0 && <p className="text-sm text-[var(--color-fg-muted)]">The room's channel creation policy keeps new channels to its owner and admins.</p>}
        <div className="flex items-center justify-between gap-4">
          <p className="text-sm font-medium">Start from a preset</p>
          <Switch checked={fromPreset} ariaLabel="Start from a preset" onChange={setFromPreset} />
        </div>
        {fromPreset && <ChannelPresetPicker roomId={roomId} channel={{ purpose: purpose.trim(), rules: "", settings: {} }} onPick={setPreset} />}
        {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}

const noop = () => {};

// ChannelSettingsDialog is a channel's settings (its managers'): name,
// purpose (with the AI's draft), rules, section, default agent, who posts,
// agents admitted, agent participation (stored only: agents answer when
// mentioned until a later version), slow mode (no default: C22).
export function ChannelSettingsDialog({
  roomId,
  subject,
  me,
  policy,
  channel: c,
  sections,
  roomAgents,
  onClose,
  onSaved,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly me: RoomMembership | null;
  readonly policy: ChannelCreationPolicy | "";
  readonly channel: Channel;
  readonly sections: readonly RoomSection[];
  readonly roomAgents: readonly RoomAgent[];
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const places = placeOptions(me, subject, policy, sections, c.sectionId);
  const [name, setName] = useState(c.name);
  const [purpose, setPurpose] = useState(c.purpose);
  const [rules, setRules] = useState(c.rules);
  const [sectionId, setSectionId] = useState(sectionOf(c, sections) ? c.sectionId : "");
  const [s, setS] = useState<ChannelSettings>(c.settings ?? {});
  const [slow, setSlow] = useState(String(c.settings?.slowModeSeconds ?? 0));
  // A forum's vocabulary, one name a line (lib/forum: ids from names).
  const [tagLines, setTagLines] = useState(linesOf(c.settings?.tags));
  const [statusLines, setStatusLines] = useState(linesOf(c.settings?.statuses));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const draft = useChannelAssist(DRAFT_CHANNEL_PURPOSE, "draft a purpose");
  // The default agent is one of the room's agents, by its key; one stored
  // that is no longer in the room is still shown (and kept) by its key.
  const current = s.defaultAgent ? { ownerId: s.defaultAgent.ownerId, core: s.defaultAgent.core || undefined, provider: s.defaultAgent.provider } : null;
  const agentChoices = useMemo(() => {
    const list = roomAgents.map((a) => ({ id: agentIdentity(a), label: agentHandle(a), ref: { ownerId: a.ownerId, core: a.core ?? "", provider: a.provider } }));
    if (current && !agentByIdentity(roomAgents, current)) list.push({ id: agentIdentity(current), label: `${agentHandle(current)} (not in the room now)`, ref: { ownerId: current.ownerId, core: current.core ?? "", provider: current.provider } });
    return list;
  }, [roomAgents, current]);

  const save = async () => {
    const seconds = Number(slow);
    if (!Number.isInteger(seconds) || seconds < 0) {
      setErr("Slow mode is a whole number of seconds (0 is off)");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await channelsApi.patch(roomId, c.channelId, {
        ...(c.isDefault ? {} : { name: name.trim() }),
        purpose: purpose.trim(),
        rules: rules.trim(),
        ...(sectionId !== c.sectionId ? { sectionId } : {}),
        settings: {
          ...s,
          slowModeSeconds: seconds,
          ...(isForum(s)
            ? { tags: tagsFromLines(tagLines, c.settings?.tags), statuses: statusesFromLines(statusLines, c.settings?.statuses) }
            : { tags: undefined, statuses: undefined, postTemplate: undefined }),
        },
      });
      onSaved();
    } catch (e) {
      setErr(errorText(e, "Could not save the channel"));
    } finally {
      setBusy(false);
    }
  };
  const suggestPurpose = async () => {
    const fields = await draft.run({ text: purposeAsk(name || c.name, purpose), roomId, channelId: c.channelId });
    if (fields) {
      const p = purposeOf(fields);
      if (p) setPurpose(p);
      else setErr("The AI did not suggest a purpose; write one by hand");
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={`#${c.name} settings`}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || (!c.isDefault && !name.trim())}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" hint={c.isDefault ? "The room's default channel keeps its name." : undefined}>
            <Input value={name} disabled={c.isDefault} onChange={(e) => setName(e.target.value)} />
          </Field>
          {places.length > 1 && (
            <Field label="Section">
              <Select value={sectionId} onChange={(e) => setSectionId(e.target.value)} aria-label="Section">
                {places.map((p) => (
                  <option key={p.value} value={p.value}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </Field>
          )}
        </div>
        <Field label="Purpose" hint={draft.available ? "One line under its name; agents read it too." : AI_UNAVAILABLE}>
          <div className="flex items-center gap-2">
            <Input value={purpose} maxLength={MaxChannelPurpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What this channel is for" />
            {draft.available && <AssistButton variant="text" label="Suggest" state={draft.state} onRun={() => void suggestPurpose()} />}
          </div>
        </Field>
        {draft.error && <p role="alert" className="-mt-2 text-xs text-[var(--color-danger)]">{draft.error}</p>}
        <Field label="Rules" hint="Shown with the room's rules in the channel's Rules sheet.">
          <Textarea rows={3} maxLength={MaxChannelRules} value={rules} onChange={(e) => setRules(e.target.value)} placeholder="How this channel is used" />
        </Field>
        <Field label="Default agent" hint="What the message box proposes here to a member who has made no choice of their own and may use it.">
          <Select
            aria-label="Default agent"
            value={current ? agentIdentity(current) : ""}
            onChange={(e) => {
              const pick = agentChoices.find((a) => a.id === e.target.value);
              setS((x) => ({ ...x, defaultAgent: pick ? pick.ref : undefined }));
            }}
          >
            <option value="">None: the room's default</option>
            {agentChoices.map((a) => (
              <option key={a.id} value={a.id}>
                {a.label} ({displayNameOf(a.ref.ownerId)})
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Who posts">
          <Segment<ChannelWhoPosts>
            value={(s.whoPosts ?? "") as ChannelWhoPosts}
            onChange={(v) => setS((x) => ({ ...x, whoPosts: v }))}
            options={[
              { value: "", label: "Everyone" },
              { value: "admins", label: "Admins", hint: "the room's owner and admins" },
            ]}
          />
        </Field>
        <div className="flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium">Agents</p>
            <p className="text-xs text-[var(--color-fg-muted)]">
              {c.isDefault ? "The default channel always admits agents." : s.noAgents ? "Agents never read this channel or answer in it." : "Agents read this channel and answer here."}
            </p>
          </div>
          <Switch checked={!s.noAgents} disabled={c.isDefault} ariaLabel="Agents admitted" onChange={(on) => setS((x) => ({ ...x, noAgents: !on }))} />
        </div>
        <Field label="Posts" hint={isForum(s) ? "Every post opens a titled thread; the channel lists its threads." : "A feed of messages."}>
          <Segment<"" | "threads">
            value={isForum(s) ? "threads" : ""}
            onChange={(v) => setS((x) => ({ ...x, postingStyle: v || undefined }))}
            options={[
              { value: "", label: "Messages" },
              { value: "threads", label: "Every post a thread" },
            ]}
          />
        </Field>
        {isForum(s) && (
          <>
            <Field label="Tags" hint="One a line. Anyone who posts tags a thread with them; the list filters by them.">
              <Textarea rows={3} value={tagLines} onChange={(e) => setTagLines(e.target.value)} placeholder={"Bug\nFeature\nQuestion"} />
            </Field>
            <Field label="Statuses" hint="One a line, in board order. Anyone who posts moves a thread between them.">
              <Textarea rows={3} value={statusLines} onChange={(e) => setStatusLines(e.target.value)} placeholder={"Open\nIn progress\nDone"} />
            </Field>
            <Field label="Post template" hint="What a new post starts from.">
              <Textarea
                rows={4}
                maxLength={MaxPostTemplate}
                value={s.postTemplate ?? ""}
                onChange={(e) => setS((x) => ({ ...x, postTemplate: e.target.value || undefined }))}
                placeholder={"What happened:\n\nSteps:\n\nExpected:"}
              />
            </Field>
          </>
        )}
        <Field
          label="Agent participation"
          hint={
            s.agentParticipation === "threads"
              ? "The channel's default agent answers every new thread once, marked automatic; anyone can stop it."
              : s.agentParticipation === "questions"
                ? "Stored for now: the agent answers when mentioned until a later version."
                : "The channel's agent answers when mentioned."
          }
        >
          <Segment<ChannelAgentParticipation>
            value={(s.agentParticipation ?? "") as ChannelAgentParticipation}
            onChange={(v) => setS((x) => ({ ...x, agentParticipation: v }))}
            options={ChannelAgentParticipationValues.map((v) => ({ value: v, label: PARTICIPATION[v] }))}
          />
        </Field>
        <Field label="Slow mode (seconds)" hint="The least time between two posts of one member here; 0 is off.">
          <Input type="number" min={0} step={1} value={slow} onChange={(e) => setSlow(e.target.value)} className="max-w-40" />
        </Field>
        {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}

// GroupDialog makes, renames or deletes one of the member's own groups of
// their rail (C24). Making one from a channel's menu puts that channel in
// it. Only the member sees it; nothing changes for anyone else.
export function GroupDialog({
  roomId,
  group,
  channel,
  groups,
  onClose,
  onSaved,
}: {
  readonly roomId: string;
  readonly group: ChannelGroup | null;
  readonly channel: Channel | null;
  readonly groups: readonly ChannelGroup[];
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const [name, setName] = useState(group?.name ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const save = async () => {
    if (!name.trim()) {
      setErr("Name is required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      if (group) {
        await channelsApi.patchGroup(roomId, group.groupId, { name: name.trim() });
      } else {
        const made = await channelsApi.createGroup(roomId, { name: name.trim(), position: groups.length });
        if (channel) await channelsApi.follow(roomId, channel.channelId, { groupId: made.groupId });
      }
      onSaved();
    } catch (e) {
      setErr(errorText(e, group ? "Could not rename the group" : "Could not make the group"));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!group) return;
    try {
      await channelsApi.removeGroup(roomId, group.groupId);
      onSaved();
    } catch (e) {
      setErr(errorText(e, "Could not delete the group"));
    }
  };
  return (
    <>
      <Dialog
        open
        onClose={onClose}
        title={group ? `Your group ${group.name}` : "New group"}
        description={group ? undefined : `Your own folder of channels${channel ? `, starting with #${channel.name}` : ""}. Only you see it; the room's sections stay as they are for everyone.`}
        footer={
          <>
            {group && (
              <Button variant="ghost" className="mr-auto text-[var(--color-danger)]" onClick={() => setConfirmDelete(true)}>
                Delete group
              </Button>
            )}
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void save()} disabled={busy || !name.trim()}>
              {busy ? "Saving…" : group ? "Save" : "Make group"}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label="Name">
            <Input
              autoFocus
              maxLength={MaxChannelGroupName}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void save()}
              placeholder="My work"
            />
          </Field>
          {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
        </div>
      </Dialog>
      <ConfirmDialog
        open={confirmDelete}
        danger
        title={`Delete your group ${group?.name ?? ""}?`}
        description="Its channels go back to their sections on your list; nothing else changes."
        confirmLabel="Delete group"
        onClose={() => setConfirmDelete(false)}
        onConfirm={remove}
      />
    </>
  );
}

// SectionDialog makes a section (section null) or changes one: name, rules,
// order, owners (comma-separated member ids) and, for a members-only
// section, who may join and its members and join requests (C11, C12, C17);
// and deletes it (its channels move to the top level). Whether a section is
// members-only is chosen when it is made.
export function SectionDialog({
  roomId,
  section,
  sections,
  onClose,
  onSaved,
}: {
  readonly roomId: string;
  readonly section: RoomSection | null;
  readonly sections: readonly RoomSection[];
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const [name, setName] = useState(section?.name ?? "");
  const [rules, setRules] = useState(section?.rules ?? "");
  const [position, setPosition] = useState(String(section?.position ?? sections.length));
  const [owners, setOwners] = useState((section?.owners ?? []).join(", "));
  const [membersOnly, setMembersOnly] = useState(section?.visibility === "members");
  const [joinPolicy, setJoinPolicy] = useState<SectionJoinPolicy>(section?.joinPolicy ?? "invite");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const save = async () => {
    const pos = Number(position);
    if (!name.trim() || !Number.isInteger(pos)) {
      setErr(!name.trim() ? "Name is required" : "Order is a whole number");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      if (section) {
        const list = owners.split(",").map((o) => o.trim()).filter(Boolean);
        await channelsApi.patchSection(roomId, section.sectionId, {
          name: name.trim(),
          rules: rules.trim(),
          position: pos,
          owners: list,
          ...(section.visibility === "members" && joinPolicy !== section.joinPolicy ? { joinPolicy } : {}),
        });
      } else {
        await channelsApi.createSection(roomId, {
          name: name.trim(),
          rules: rules.trim(),
          position: pos,
          ...(membersOnly ? { visibility: "members" as const, joinPolicy } : {}),
        });
      }
      onSaved();
    } catch (e) {
      setErr(errorText(e, section ? "Could not save the section" : "Could not create the section"));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!section) return;
    try {
      await channelsApi.removeSection(roomId, section.sectionId);
      onSaved();
    } catch (e) {
      setErr(errorText(e, "Could not delete the section"));
    }
  };
  return (
    <>
      <Dialog
        open
        onClose={onClose}
        title={section ? `Section ${section.name}` : "New section"}
        description={section ? undefined : "A named group of channels. You own it: you and the room's owner and admins change it."}
        footer={
          <>
            {section && (
              <Button variant="ghost" className="mr-auto text-[var(--color-danger)]" onClick={() => setConfirmDelete(true)}>
                Delete section
              </Button>
            )}
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void save()} disabled={busy || !name.trim()}>
              {busy ? "Saving…" : section ? "Save" : "Create section"}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label="Name">
            <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Design team" />
          </Field>
          <Field label="Rules" hint="Shown with the room's and the channel's rules in each of its channels.">
            <Textarea rows={3} maxLength={MaxChannelRules} value={rules} onChange={(e) => setRules(e.target.value)} />
          </Field>
          <Field label="Order" hint="Sections are listed by this number, smallest first.">
            <Input type="number" step={1} value={position} onChange={(e) => setPosition(e.target.value)} className="max-w-32" />
          </Field>
          {section && (
            <Field label="Owners" hint="Members who change this section and the channels in it, by their ids, comma-separated.">
              <Input value={owners} onChange={(e) => setOwners(e.target.value)} />
            </Field>
          )}
          {!section && (
            <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-1" checked={membersOnly} onChange={(e) => setMembersOnly(e.target.checked)} />
              <span>
                Members only
                <span className="block text-xs text-[var(--color-fg-muted)]">
                  Only its members see its channels, their messages and files. You are its first member; the room&rsquo;s owner and admins see its name and members, and read it only by joining. This can&rsquo;t be changed later.
                </span>
              </span>
            </label>
          )}
          {(section ? section.visibility === "members" : membersOnly) && (
            <Field label="Who may join" hint="The rest of the room sees nothing of an invite-only section; for the others they see its name, never its channels.">
              <Select value={joinPolicy} onChange={(e) => setJoinPolicy(e.target.value as SectionJoinPolicy)} aria-label="Who may join">
                <option value="invite">Only those its owners add</option>
                <option value="ask">Members ask; its owners approve</option>
                <option value="open">Any member joins</option>
              </Select>
            </Field>
          )}
          {section?.visibility === "members" && <SectionMembers roomId={roomId} section={section} onChanged={onSaved} />}
          {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
        </div>
      </Dialog>
      <ConfirmDialog
        open={confirmDelete}
        danger
        title={`Delete the section ${section?.name ?? ""}?`}
        description="Its channels move to the room's top level; nothing else is deleted."
        confirmLabel="Delete section"
        onClose={() => setConfirmDelete(false)}
        onConfirm={remove}
      />
    </>
  );
}

// SectionMembers lists a members-only section's members and its open join
// requests to those who manage it (its owners, the room's owner and admins:
// the server answers 403 to anyone else, and the list is then left out):
// add a member by id, remove one, approve or decline a request.
function SectionMembers({ roomId, section, onChanged }: { readonly roomId: string; readonly section: RoomSection; readonly onChanged: () => void }) {
  const [requests, setRequests] = useState<SectionJoinRequest[] | null>(null);
  const [add, setAdd] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    channelsApi
      .sectionRequests(roomId, section.sectionId)
      .then((r) => live && setRequests(r.requests))
      .catch(() => live && setRequests(null));
    return () => {
      live = false;
    };
  }, [roomId, section.sectionId]);
  const run = async (what: string, fn: () => Promise<unknown>) => {
    setErr("");
    try {
      await fn();
      onChanged();
    } catch (e) {
      setErr(errorText(e, `Could not ${what}`));
    }
  };
  const members = section.members ?? [];
  return (
    <div className="flex flex-col gap-2" data-section-members>
      <div className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Members ({members.length})</div>
      <ul className="flex flex-col gap-1 text-sm">
        {members.map((m) => (
          <li key={m} className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate">{m}</span>
            {!section.owners.includes(m) && (
              <Button size="sm" variant="ghost" onClick={() => void run("remove the member", () => channelsApi.removeSectionMember(roomId, section.sectionId, m))}>
                Remove
              </Button>
            )}
          </li>
        ))}
      </ul>
      <div className="flex items-center gap-2">
        <Input value={add} onChange={(e) => setAdd(e.target.value)} placeholder="Member id" aria-label="Add a member" />
        <Button size="sm" variant="outline" disabled={!add.trim()} onClick={() => void run("add the member", () => channelsApi.addSectionMember(roomId, section.sectionId, add.trim()))}>
          Add
        </Button>
      </div>
      {requests && requests.length > 0 && (
        <div className="flex flex-col gap-1" data-section-requests>
          <div className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Asking to join</div>
          {requests.map((q) => (
            <div key={q.userId} className="flex items-center gap-2 text-sm">
              <span className="min-w-0 flex-1 truncate">{q.userId}</span>
              <Button size="sm" variant="primary" onClick={() => void run("approve the request", () => channelsApi.addSectionMember(roomId, section.sectionId, q.userId))}>
                Approve
              </Button>
              <Button size="sm" variant="ghost" onClick={() => void run("decline the request", () => channelsApi.declineSectionRequest(roomId, section.sectionId, q.userId))}>
                Decline
              </Button>
            </div>
          ))}
        </div>
      )}
      {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
    </div>
  );
}
