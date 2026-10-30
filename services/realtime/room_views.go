package realtimehttp

import (
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// initViews makes the views of who is here and which agents serve the room,
// and the trackers of this replica's streams that feed them.
func (s *Service) initViews() {
	var announce func(wire.Event)
	if s.Hub != nil && s.Hub.Bus != nil {
		announce = s.Hub.Announce
	}
	s.Views = realtime.NewRoomViews(announce, func(ev wire.Event) {
		if s.Hub != nil {
			s.Hub.PublishLocal(ev)
		}
	})
	if s.Presence == nil {
		s.Presence = realtime.NewPresence(func(ev wire.Event) { s.Views.LocalPresence(ev.RoomID, s.Presence.Members(ev.RoomID)) })
	}
	if s.Roster == nil {
		s.Roster = realtime.NewAgentRoster(func(ev wire.Event) { s.Views.LocalRoster(ev.RoomID, s.Roster.Snapshot(ev.RoomID)) })
	}
	s.viewsStop = make(chan struct{})
	go s.Views.Run(s.viewsStop)
}
