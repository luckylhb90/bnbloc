package component

// RelaySessionPath is the relay's own route that answers the bound turn
// (GET <relay>/session): RelaySession. It is the bridge's, never a gateway
// route. The relay's address and bearer reach the tool in
// pkgmeta.RoomRelayURLEnv and pkgmeta.RoomRelayTokenEnv.
const RelaySessionPath = "/session"

// RelaySession is the turn a room-credential tool's session is driving now,
// as the relay knows it: who the calls act for (Requester), whose machine
// and agent it is (Owner), where the turn was asked (Room, Channel) and by
// which message (MessageSeq, increasing in the room). ByRequester marks a
// turn its requester opened with their own typed message (not a schedule's
// post, not another agent's dispatch): a tool that paused for a person's
// answer accepts it only in such a turn, newer than the pause.
type RelaySession struct {
	JobID       string `json:"jobId"`
	RoomID      string `json:"roomId"`
	ChannelID   string `json:"channelId,omitempty"`
	Requester   string `json:"requester"`
	Owner       string `json:"owner"`
	MessageSeq  int64  `json:"messageSeq"`
	ByRequester bool   `json:"byRequester"`
}
