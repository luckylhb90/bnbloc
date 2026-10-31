// Shapes the e2e mocks answer with, complete as the server sends them
// (internal/wire RoomDetail, RoomChannel): a mock missing a field the
// client reads crashes the view, and the check fails for the wrong reason.
// Pass what a script needs to differ.

export const room = (over = {}) => ({
  roomId: "r1", name: "r1", description: "", visibility: "private", previewable: false, syncMode: "live",
  createdBy: "maya", createdAtMs: 1, memberCount: 1, channelCreation: "anyone", welcome: "", quickPrompts: [],
  rules: "", rulesVersion: 0, requireRulesAgreement: false, recommendedChannels: [], ...over,
});

export const channel = (over = {}) => ({
  channelId: "general", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "",
  sectionId: "", position: 0, ownerId: "maya", settings: {}, joined: true, starred: false, hidden: false, groupId: "", ...over,
});
