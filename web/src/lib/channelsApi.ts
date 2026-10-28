// A room's channels, sections, directory, rules and following: the room
// service's routes (docs/architecture/room-channels.md, "API"). The rules of
// who may call each are the server's; lib/channelRules.ts mirrors them for
// what the controls offer.
import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import type {
  ChannelCreateRequest,
  ChannelDirectory,
  ChannelFollowRequest,
  ChannelGroup,
  ChannelGroupCreateRequest,
  ChannelGroupPatchRequest,
  ChannelList,
  ChannelPatchRequest,
  RoomChannel,
  RoomDetail,
  RoomPatchRequest,
  RoomRulesState,
  RoomSection,
  SectionCreateRequest,
  SectionJoinReply,
  SectionPatchRequest,
  SectionRequests,
} from "./wire.gen.ts";

const json = (method: string, body: unknown) => ({ method, body: JSON.stringify(body) });

export const channelsApi = {
  /** The open channels and the sections; archived: the archived channels. */
  list: (roomId: string, archived = false) => req<ChannelList>(`${roomPath(roomId, "channels")}${archived ? "?archived=1" : ""}`),
  directory: (roomId: string) => req<ChannelDirectory>(roomPath(roomId, "channel-directory")),
  create: (roomId: string, body: ChannelCreateRequest) => req<RoomChannel>(roomPath(roomId, "channels"), json("POST", body)),
  patch: (roomId: string, channelId: string, body: ChannelPatchRequest) => req<RoomChannel>(roomPath(roomId, "channels", channelId), json("PATCH", body)),
  archive: (roomId: string, channelId: string, archived: boolean) =>
    req<RoomChannel>(roomPath(roomId, "channels", channelId, archived ? "archive" : "restore"), { method: "POST" }),
  remove: (roomId: string, channelId: string) => req<void>(roomPath(roomId, "channels", channelId), { method: "DELETE", as: "none" }),
  follow: (roomId: string, channelId: string, marks: ChannelFollowRequest = {}) => req<RoomChannel>(roomPath(roomId, "channels", channelId, "follow"), json("PUT", marks)),
  unfollow: (roomId: string, channelId: string) => req<RoomChannel>(roomPath(roomId, "channels", channelId, "follow"), { method: "DELETE" }),
  createSection: (roomId: string, body: SectionCreateRequest) => req<RoomSection>(roomPath(roomId, "sections"), json("POST", body)),
  patchSection: (roomId: string, sectionId: string, body: SectionPatchRequest) => req<RoomSection>(roomPath(roomId, "sections", sectionId), json("PATCH", body)),
  removeSection: (roomId: string, sectionId: string) => req<void>(roomPath(roomId, "sections", sectionId), { method: "DELETE", as: "none" }),
  /** A members-only section's membership (C11, C12, C17): join (at once, or
   *  a request its owners approve), leave, add or remove a member, list and
   *  decline the requests. */
  joinSection: (roomId: string, sectionId: string) => req<SectionJoinReply>(roomPath(roomId, "sections", sectionId, "join"), { method: "POST" }),
  leaveSection: (roomId: string, sectionId: string) => req<void>(roomPath(roomId, "sections", sectionId, "join"), { method: "DELETE", as: "none" }),
  addSectionMember: (roomId: string, sectionId: string, userId: string) =>
    req<RoomSection>(roomPath(roomId, "sections", sectionId, "members"), json("POST", { userId })),
  removeSectionMember: (roomId: string, sectionId: string, userId: string) =>
    req<void>(roomPath(roomId, "sections", sectionId, "members", userId), { method: "DELETE", as: "none" }),
  sectionRequests: (roomId: string, sectionId: string) => req<SectionRequests>(roomPath(roomId, "sections", sectionId, "requests")),
  declineSectionRequest: (roomId: string, sectionId: string, userId: string) =>
    req<void>(roomPath(roomId, "sections", sectionId, "requests", userId), { method: "DELETE", as: "none" }),
  /** The member's own groups of their rail (C24): only they see them. */
  createGroup: (roomId: string, body: ChannelGroupCreateRequest) => req<ChannelGroup>(roomPath(roomId, "channel-groups"), json("POST", body)),
  patchGroup: (roomId: string, groupId: string, body: ChannelGroupPatchRequest) => req<ChannelGroup>(roomPath(roomId, "channel-groups", groupId), json("PATCH", body)),
  removeGroup: (roomId: string, groupId: string) => req<void>(roomPath(roomId, "channel-groups", groupId), { method: "DELETE", as: "none" }),
  rules: (roomId: string) => req<RoomRulesState>(roomPath(roomId, "rules")),
  agree: (roomId: string, version: number) => req<RoomRulesState>(roomPath(roomId, "rules", "agree"), json("POST", { version })),
  /** The room's rules, agreement switch, recommended channels and creation
   *  policy (the room's owner or an admin). */
  setup: (roomId: string, body: Pick<RoomPatchRequest, "rules" | "requireRulesAgreement" | "recommendedChannels" | "channelCreation">) =>
    req<RoomDetail>(roomPath(roomId), json("PATCH", body)),
};
