package store

import (
	"context"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// The channel store holds the section memberships (SectionMembershipStore):
// the memory view shares MemoryRoom's mutex, so DeleteRoom clears them with
// the room's other rows.

var (
	_ SectionMembershipStore = (*MemoryChannel)(nil)
	_ SectionMembershipStore = (*MySQLChannel)(nil)
)

func sectionMemberKey(tenantID, roomID, sectionID, userID string) string {
	return tenantID + "\x00" + roomID + "\x00" + sectionID + "\x00" + userID
}

func (s *MemoryChannel) AddSectionMember(ctx context.Context, m SectionMember) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := sectionMemberKey(tnt, m.RoomID, m.SectionID, m.UserID)
	if _, ok := s.room.sectionMembers[k]; !ok {
		m.TenantID = tnt
		s.room.sectionMembers[k] = m
	}
	delete(s.room.sectionRequests, k)
	return nil
}

func (s *MemoryChannel) RemoveSectionMember(ctx context.Context, roomID, sectionID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := sectionMemberKey(tnt, roomID, sectionID, userID)
	if _, ok := s.room.sectionMembers[k]; !ok {
		return ErrNotSectionMember
	}
	delete(s.room.sectionMembers, k)
	return nil
}

func (s *MemoryChannel) SectionMembers(ctx context.Context, roomID, sectionID string) ([]SectionMember, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := []SectionMember{}
	for _, m := range s.room.sectionMembers {
		if m.TenantID == tnt && m.RoomID == roomID && m.SectionID == sectionID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].JoinedAtMs != out[j].JoinedAtMs {
			return out[i].JoinedAtMs < out[j].JoinedAtMs
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (s *MemoryChannel) MemberSections(ctx context.Context, roomID, userID string) ([]string, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := []string{}
	for _, m := range s.room.sectionMembers {
		if m.TenantID == tnt && m.RoomID == roomID && m.UserID == userID {
			out = append(out, m.SectionID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryChannel) PutSectionJoinRequest(ctx context.Context, q SectionJoinRequest) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := sectionMemberKey(tnt, q.RoomID, q.SectionID, q.UserID)
	if _, ok := s.room.sectionRequests[k]; !ok {
		q.TenantID = tnt
		s.room.sectionRequests[k] = q
	}
	return nil
}

func (s *MemoryChannel) SectionJoinRequests(ctx context.Context, roomID, sectionID string) ([]SectionJoinRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	out := []SectionJoinRequest{}
	for _, q := range s.room.sectionRequests {
		if q.TenantID == tnt && q.RoomID == roomID && q.SectionID == sectionID {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestedAtMs != out[j].RequestedAtMs {
			return out[i].RequestedAtMs < out[j].RequestedAtMs
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (s *MemoryChannel) TakeSectionJoinRequest(ctx context.Context, roomID, sectionID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	k := sectionMemberKey(tnt, roomID, sectionID, userID)
	if _, ok := s.room.sectionRequests[k]; !ok {
		return ErrNoJoinRequest
	}
	delete(s.room.sectionRequests, k)
	return nil
}

// AddSectionMember inserts the membership (a repeat is kept as it was) and
// closes the member's request, in one transaction.
func (m *MySQLChannel) AddSectionMember(ctx context.Context, sm SectionMember) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO section_members (tenant_id, room_id, section_id, user_id, added_by, joined_at_ms)
VALUES (?, ?, ?, ?, ?, ?)`, tnt, sm.RoomID, sm.SectionID, sm.UserID, sm.AddedBy, sm.JoinedAtMs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM section_join_requests WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND user_id = ?`,
		tnt, sm.RoomID, sm.SectionID, sm.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *MySQLChannel) RemoveSectionMember(ctx context.Context, roomID, sectionID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `
DELETE FROM section_members WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND user_id = ?`,
		tnt, roomID, sectionID, userID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrNotSectionMember)
}

func scanSectionMember(sc rowScanner) (SectionMember, error) {
	var sm SectionMember
	err := sc.Scan(&sm.TenantID, &sm.RoomID, &sm.SectionID, &sm.UserID, &sm.AddedBy, &sm.JoinedAtMs)
	return sm, err
}

func (m *MySQLChannel) SectionMembers(ctx context.Context, roomID, sectionID string) ([]SectionMember, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT tenant_id, room_id, section_id, user_id, added_by, joined_at_ms FROM section_members
WHERE tenant_id = ? AND room_id = ? AND section_id = ? ORDER BY joined_at_ms ASC, user_id ASC`,
		scanSectionMember, tnt, roomID, sectionID)
	if out == nil && err == nil {
		out = []SectionMember{}
	}
	return out, err
}

func (m *MySQLChannel) MemberSections(ctx context.Context, roomID, userID string) ([]string, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT section_id FROM section_members WHERE tenant_id = ? AND room_id = ? AND user_id = ? ORDER BY section_id`,
		func(sc rowScanner) (string, error) {
			var id string
			return id, sc.Scan(&id)
		}, tnt, roomID, userID)
	if out == nil && err == nil {
		out = []string{}
	}
	return out, err
}

func (m *MySQLChannel) PutSectionJoinRequest(ctx context.Context, q SectionJoinRequest) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `
INSERT IGNORE INTO section_join_requests (tenant_id, room_id, section_id, user_id, requested_at_ms)
VALUES (?, ?, ?, ?, ?)`, tnt, q.RoomID, q.SectionID, q.UserID, q.RequestedAtMs)
	return err
}

func (m *MySQLChannel) SectionJoinRequests(ctx context.Context, roomID, sectionID string) ([]SectionJoinRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, m.DB, `
SELECT tenant_id, room_id, section_id, user_id, requested_at_ms FROM section_join_requests
WHERE tenant_id = ? AND room_id = ? AND section_id = ? ORDER BY requested_at_ms ASC, user_id ASC`,
		func(sc rowScanner) (SectionJoinRequest, error) {
			var q SectionJoinRequest
			return q, sc.Scan(&q.TenantID, &q.RoomID, &q.SectionID, &q.UserID, &q.RequestedAtMs)
		}, tnt, roomID, sectionID)
	if out == nil && err == nil {
		out = []SectionJoinRequest{}
	}
	return out, err
}

func (m *MySQLChannel) TakeSectionJoinRequest(ctx context.Context, roomID, sectionID, userID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `
DELETE FROM section_join_requests WHERE tenant_id = ? AND room_id = ? AND section_id = ? AND user_id = ?`,
		tnt, roomID, sectionID, userID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrNoJoinRequest)
}
