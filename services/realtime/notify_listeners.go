package realtimehttp

import (
	"context"
	"sort"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// hereWindow is how recently a member read a channel to count as here for an
// @here (C5).
const hereWindow = time.Hour

// notifyListeners is notify.Observer.Listeners: the people listening to a
// channel, each a person in the room who may read it (store.MayReadChannel),
// with their effective level (wire.EffectiveNotifyLevel, the one resolver)
// and whether they are here (follow the channel, read it in the last hour,
// or are in the room now).
func (s *Service) notifyListeners(ctx context.Context, roomID, channelID, reach string) ([]notify.Listener, error) {
	if s.Channels == nil {
		return nil, nil
	}
	ch, ok, err := s.Channels.GetChannel(ctx, roomID, channelID)
	if err != nil || !ok {
		return nil, err
	}
	follows, err := s.Channels.ChannelFollowersOf(ctx, roomID, channelID)
	if err != nil {
		return nil, err
	}
	byUser := make(map[string]store.ChannelFollow, len(follows))
	candidates := map[string]bool{}
	for _, f := range follows {
		byUser[f.UserID] = f
		candidates[f.UserID] = true
	}
	here := map[string]bool{}
	if reach == notify.ReachHere {
		if s.Chat != nil {
			readers, err := s.Chat.ChannelReadersSince(ctx, roomID, channelID, time.Now().Add(-hereWindow).UnixMilli())
			if err != nil {
				return nil, err
			}
			for _, u := range readers {
				here[u], candidates[u] = true, true
			}
		}
		if s.Presence != nil {
			for _, u := range s.Presence.Members(roomID) {
				here[u], candidates[u] = true, true
			}
		}
	}
	if reach == notify.ReachChannel && s.RoomRoles != nil {
		members, err := s.RoomRoles.Members(ctx, roomID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if !m.AgentHost {
				candidates[m.UserID] = true
			}
		}
	}
	out := make([]notify.Listener, 0, len(candidates))
	for u := range candidates {
		if !membership.HumanMember(s.Membership, roomID, u) || !s.memberMayReadChannel(ctx, roomID, channelID, u) {
			continue
		}
		f, following := byUser[u]
		level := wire.EffectiveNotifyLevel(f.Level, ch.IsThread(), following, f.LastPostedAtMs > 0)
		out = append(out, notify.Listener{UserID: u, Level: level, Here: following || here[u]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}
