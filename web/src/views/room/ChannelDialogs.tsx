import { useMemo, useState } from "react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { DRAFT_CHANNEL_PURPOSE, AI_UNAVAILABLE, purposeAsk, purposeOf } from "@/lib/channelAssists";
import { mayPlaceChannel, sectionOf } from "@/lib/channelRules";
import { errorText } from "@/lib/errors";
import { agentByIdentity, agentHandle, agentIdentity } from "@/lib/roomAgents";
import type { RoomMembership } from "@/lib/roomRoles";
import { displayNameOf } from "@/lib/useDisplayName";
import {
  ChannelAgentParticipationValues,
  MaxChannelGroupName,
  MaxChannelPurpose,
  MaxChannelRules,
  type ChannelAgentParticipation,
  type ChannelCreationPolicy,
  type ChannelGroup,
  type ChannelSettings,
  type ChannelWhoPosts,
  type RoomSection,
} from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { AssistButton } from "@/views/AssistButton";
import type { RoomAgent } from "@/views/composer/types";
import { useChannelAssist } from "@/views/room/useChannelAssist";

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
  const create = async () => {
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr("");
    try {
      onCreated(await channelsApi.create(roomId, { name: name.trim(), purpose: purpose.trim(), sectionId }));
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
        {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}

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
        settings: { ...s, slowModeSeconds: seconds },
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
        <Field label="Agent participation" hint="Stored for now: until a later version, the channel's agent answers when mentioned, whatever this says.">
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

// SectionDialog makes a section (section null) or changes one: name, rules,
// order and owners (comma-separated member ids), and deletes it (its
// channels move to the top level). Slice 1 makes open sections only.
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
        await channelsApi.patchSection(roomId, section.sectionId, { name: name.trim(), rules: rules.trim(), position: pos, owners: list });
      } else {
        await channelsApi.createSection(roomId, { name: name.trim(), rules: rules.trim(), position: pos });
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
