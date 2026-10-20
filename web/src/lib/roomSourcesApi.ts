import { req } from "./api.ts";
import { useAuth } from "./auth";
import { roomPath } from "./roomId.ts";
import type { RoomSourceDetail, RoomSourceGrant, RoomSourcesInfo, RoomSourceScope } from "./roomSources.ts";
import { invalidateQueries, queryKey, useQuery, type QueryResult } from "./useQuery";
import type { RoomReportReply, RoomSource, RoomSourceGrantsReply } from "./wire.gen.ts";

const enc = encodeURIComponent;
const tenantPath = (roomId: string, source: RoomSource) => `/v1/tenant/room-sources/${enc(roomId)}/${enc(source)}`;

// Room sources (services/gateway/room_sources.go): a room's asks, who
// reviews it and its owner's opt-in (any member reads them), asking the
// organization's admins, and the admin console's asks, grants (read by the
// room's owner) and acknowledgments of changed tasks.
export const roomSourcesApi = {
  info: (roomId: string) => req<RoomSourcesInfo>(roomPath(roomId, "sources")),
  optIn: (roomId: string, source: RoomSource, optedIn: boolean) =>
    req<RoomSourcesInfo>(roomPath(roomId, "sources", source, "opt-in"), { method: "PUT", body: JSON.stringify({ optedIn }) }),
  askAdmin: (roomId: string, source: RoomSource) => req<RoomReportReply>(roomPath(roomId, "sources", source, "ask-admin"), { method: "POST" }),
  grants: () => req<RoomSourceGrantsReply>("/v1/tenant/room-sources"),
  detail: (roomId: string, source: RoomSource) => req<RoomSourceDetail>(tenantPath(roomId, source)),
  grant: (roomId: string, source: RoomSource, scope: RoomSourceScope, grantee: string) =>
    req<RoomSourceGrant>(tenantPath(roomId, source), { method: "PUT", body: JSON.stringify({ scope, grantee }) }),
  withdraw: (roomId: string, source: RoomSource) => req<RoomSourceGrant>(tenantPath(roomId, source), { method: "DELETE" }),
  acknowledge: (roomId: string, source: RoomSource) => req<RoomSourceDetail>(`${tenantPath(roomId, source)}/ack`, { method: "POST" }),
};

const ROOM_SOURCES = "room-sources:";

// useRoomSources is GET /v1/rooms/{id}/sources: the header's note and the
// settings' opt-in read the one copy; refreshRoomSources after a change.
export function useRoomSources(roomId: string): QueryResult<RoomSourcesInfo> {
  const { subject, tenant } = useAuth();
  return useQuery(roomId ? queryKey(ROOM_SOURCES + roomId, { subject, tenant }) : null, () => roomSourcesApi.info(roomId), { staleMs: 60_000 });
}

export function refreshRoomSources(): void {
  invalidateQueries(ROOM_SOURCES);
}
