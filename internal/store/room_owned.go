package store

// roomOwnedTable is one table holding rows a room owns. DeleteRoom clears
// every entry of roomOwnedTables in one transaction, in order: a table cleared
// through a parent (leases through grants, versions through artifacts, reply
// references through reply tokens) comes before its parent.
type roomOwnedTable struct {
	table string
	// where selects the room's rows; args binds its placeholders.
	where string
	args  func(tenantID, roomID string) []any
	// destroy, when set, is the SET clause DeleteRoom's transaction runs on
	// the table's rows instead of deleting them: a crypto-shred destroys
	// the app keys with the room, atomically (appKeyDestroyed).
	destroy string
	// batched tables are deleted after the transaction commits, in batches
	// (deleteInBatches), in list order: their rows are unreadable by then
	// (their key was destroyed in it), and a large app never holds one long
	// transaction.
	batched bool
}

func tenantRoomArgs(tenantID, roomID string) []any { return []any{tenantID, roomID} }

func tenantTenantRoomArgs(tenantID, roomID string) []any { return []any{tenantID, tenantID, roomID} }

func roomOnlyArgs(_, roomID string) []any { return []any{roomID} }

// byTenantRoom is a table keyed on (tenant_id, room_id).
func byTenantRoom(table string) roomOwnedTable {
	return roomOwnedTable{table: table, where: `tenant_id = ? AND room_id = ?`, args: tenantRoomArgs}
}

// roomOwnedTables is the one list of room-owned tables: DeleteRoom (MySQL)
// clears exactly these, TestRoomOwnedTablesDeletedWithRoom checks that every
// schema.sql table holding a room id is listed (or exempted with a reason),
// and the memory stores implement the same set through roomRowsOwner.
var roomOwnedTables = []roomOwnedTable{
	// Cleared through a parent, before the parent goes.
	{table: "artifact_versions", args: tenantTenantRoomArgs,
		where: `tenant_id = ? AND artifact_id IN (SELECT artifact_id FROM artifacts WHERE tenant_id = ? AND room_id = ?)`},
	// Reply references are cross-tenant rows keyed by the token they name.
	{table: "notification_reply_refs", args: tenantRoomArgs,
		where: `token_hash IN (SELECT token_hash FROM notification_reply_tokens WHERE tenant_id = ? AND room_id = ?)`},

	byTenantRoom("room_members"),
	byTenantRoom("room_messages"),
	byTenantRoom("room_read_state"),
	byTenantRoom("room_invites"),
	byTenantRoom("room_channels"),
	// A task's title is a snapshot of the message text: it must not outlive
	// the message.
	byTenantRoom("message_tasks"),
	byTenantRoom("agent_sessions"),
	byTenantRoom("agent_messages"),
	byTenantRoom("agent_jobs"),
	byTenantRoom("sync_states"),
	byTenantRoom("shared_agent_grants"),
	byTenantRoom("shared_agent_audit"),
	byTenantRoom("shared_agent_quota_requests"),
	// Attachment rows are metadata; their bytes live in the content-addressed
	// blob store, which the blob collection clears once no row references them.
	byTenantRoom("attachments"),
	byTenantRoom("artifacts"),
	byTenantRoom("artifact_shares"),
	byTenantRoom("user_agent_prefs"),
	byTenantRoom("room_imports"),
	byTenantRoom("room_schedules"),
	byTenantRoom("notification_reply_tokens"),
	byTenantRoom("content_drafts"),
	byTenantRoom("content_room_shares"),
	// Room-scoped enables and the room expert (scope_id is the room id).
	{table: "content_component_uses", args: roomOnlyArgs,
		where: `scope_kind IN ('` + string(ContentUseRoom) + `', '` + string(ContentUseRoomExpert) + `') AND scope_id = ?`},
	// Default-room grants of organization sync (room ids are global).
	{table: "org_sync_default_rooms", args: roomOnlyArgs, where: `room_id = ?`},
	// Interactive apps' data is crypto-shredded: the transaction destroys
	// the room's app keys, then the documents (through their app's key
	// row) and the key rows go in batches.
	{table: "app_docs", batched: true, args: tenantTenantRoomArgs,
		where: `tenant_id = ? AND app_id IN (SELECT app_id FROM app_keys WHERE tenant_id = ? AND room_id = ?)`},
	{table: "app_keys", destroy: appKeyDestroyed, batched: true, args: tenantRoomArgs, where: `tenant_id = ? AND room_id = ?`},
}

// roomOwnedExempt are schema.sql tables that hold a room id but are not
// cleared with the room, each with the reason.
var roomOwnedExempt = map[string]string{
	"rooms": "the room row itself is soft-deleted and kept, so its id is never reused",
}

// roomRowsOwner is a memory store holding rows a room owns. MemoryRoom's
// DeleteRoom calls every owner NewBackends wired to it, so the memory backends
// clear what the MySQL DeleteRoom clears.
type roomRowsOwner interface {
	deleteRoomRows(tenantID, roomID string)
}
