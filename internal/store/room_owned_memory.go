package store

import "strings"

// The memory side of roomOwnedTables: each memory store holding room-owned
// rows clears them when MemoryRoom.DeleteRoom cascades (see roomRowsOwner).

// roomKeyPrefix is the "tenant\x00room\x00" prefix of composite memory keys.
func roomKeyPrefix(tenantID, roomID string) string { return tenantID + "\x00" + roomID + "\x00" }

// room_messages, room_counters, room_read_state.
func (s *MemoryChat) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tenantID + "\x00" + roomID
	delete(s.msgs, rk)
	delete(s.last, roomID)
	prefix := rk + "\x00"
	for _, m := range []map[string]int64{s.read, s.byCMI} {
		for k := range m {
			if strings.HasPrefix(k, prefix) {
				delete(m, k)
			}
		}
	}
	for k := range s.deleted {
		if strings.HasPrefix(k, prefix) {
			delete(s.deleted, k)
		}
	}
}

// message_tasks.
func (s *MemoryMessageTasks) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, tenantID+"\x00"+roomID)
}

// agent_sessions, agent_messages, sync_states.
func (m *Memory) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := func(k sessionKey) bool { return k.tenant == tenantID && k.room == roomID }
	for k := range m.versions {
		if in(k) {
			delete(m.versions, k)
		}
	}
	for k := range m.cursors {
		if in(k) {
			delete(m.cursors, k)
		}
	}
	kept := m.usage[:0]
	for _, u := range m.usage {
		if u.tenant != tenantID || u.room != roomID {
			kept = append(kept, u)
		}
	}
	m.usage = kept
}

// agent_jobs.
func (s *MemoryBridgeJobs) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		if j.TenantID == tenantID && j.RoomID == roomID {
			delete(s.jobs, id)
		}
	}
	for k, id := range s.origin {
		if _, live := s.jobs[id]; !live {
			delete(s.origin, k)
		}
	}
}

// shared_agent_grants, shared_agent_audit.
func (s *MemoryGrant) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, g := range s.grants {
		if g.TenantID == tenantID && g.RoomID == roomID {
			delete(s.grants, id)
		}
	}
	kept := s.audit[:0]
	for _, r := range s.audit {
		if r.TenantID != tenantID || r.RoomID != roomID {
			kept = append(kept, r)
		}
	}
	s.audit = kept
}

// shared_agent_quota_requests.
func (s *MemoryQuotaRequests) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.reqs {
		if r.TenantID == tenantID && r.RoomID == roomID {
			delete(s.reqs, k)
		}
	}
	for pk, k := range s.pending {
		if _, live := s.reqs[k]; !live {
			delete(s.pending, pk)
		}
	}
}

// attachments.
func (s *MemoryAttachments) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := roomKeyPrefix(tenantID, roomID)
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			delete(s.m, k)
		}
	}
}

// artifacts, artifact_versions, artifact_shares.
func (s *MemoryArtifacts) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.artifacts {
		if a.TenantID == tenantID && a.RoomID == roomID {
			delete(s.artifacts, id)
			delete(s.versions, id)
		}
	}
	for id, sh := range s.shares {
		if sh.TenantID == tenantID && sh.RoomID == roomID {
			delete(s.shares, id)
		}
	}
}

// user_agent_prefs.
func (m *MemoryAgentPrefs) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.rows {
		if k[0] == tenantID && k[2] == roomID {
			delete(m.rows, k)
		}
	}
}

// room_imports.
func (m *MemoryRoomImports) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, [2]string{tenantID, roomID})
}

// room_schedules.
func (m *MemoryRoomSchedules) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.rows {
		if row.s.TenantID == tenantID && row.s.RoomID == roomID {
			delete(m.rows, id)
		}
	}
}

// notification_reply_tokens, notification_reply_refs.
func (m *MemoryNotificationReplies) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, t := range m.rows {
		if t.TenantID == tenantID && t.RoomID == roomID {
			delete(m.rows, h)
		}
	}
	for k, r := range m.refs {
		if _, live := m.rows[r.TokenHash]; !live {
			delete(m.refs, k)
		}
	}
}

// content_drafts, content_room_shares, room-scoped content_component_uses.
func (s *MemoryContentComponents) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, d := range s.drafts {
		if d.TenantID == tenantID && d.RoomID == roomID {
			delete(s.drafts, id)
		}
	}
	for k, sh := range s.roomShares {
		if sh.RoomID == roomID {
			delete(s.roomShares, k)
		}
	}
	for k, u := range s.uses {
		if (u.ScopeKind == ContentUseRoom || u.ScopeKind == ContentUseRoomExpert) && u.ScopeID == roomID {
			delete(s.uses, k)
		}
	}
}

// org_sync_default_rooms.
func (s *MemoryOrgSync) deleteRoomRows(_, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.rooms {
		if strings.HasSuffix(k, "\x00"+roomID) {
			delete(s.rooms, k)
		}
	}
}
