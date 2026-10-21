package notify

import (
	"net/url"
	"strconv"
	"strings"
)

// MessageURL is the web deep link to one room message (G3):
// <origin>/#/room/<roomId>?seq=<n>[&channel=<channelId>]. The web client
// opens the room, switches to the channel, pages history until the message
// is loaded, then scrolls to and highlights it. seq <= 0 links to the room;
// "" when no public URL is set.
func MessageURL(publicURL, roomID, channelID string, seq int64) string {
	u := RoomURL(publicURL, roomID)
	if u == "" || roomID == "" || seq <= 0 {
		return u
	}
	u += "?seq=" + strconv.FormatInt(seq, 10)
	if c := strings.TrimSpace(channelID); c != "" {
		u += "&channel=" + url.QueryEscape(c)
	}
	return u
}

// ArtifactURL is the web deep link to one room deliverable:
// <origin>/#/room/<roomId>?artifact=<artifactId>[&v=<n>]. The web client
// opens the room and the preview workbench on it; version <= 0 links to the
// latest. "" when no public URL is set.
func ArtifactURL(publicURL, roomID, artifactID string, version int) string {
	u := RoomURL(publicURL, roomID)
	if u == "" || roomID == "" || artifactID == "" {
		return u
	}
	u += "?artifact=" + url.QueryEscape(artifactID)
	if version > 0 {
		u += "&v=" + strconv.Itoa(version)
	}
	return u
}

// urlHash returns the hash route of a deep link ("#/room/…"), or the room's
// route when the link is empty (no public URL configured): a browser push
// notification opens it relative to the web client's own origin.
func urlHash(link, roomID string) string {
	if i := strings.Index(link, "#"); i >= 0 {
		return link[i:]
	}
	if roomID == "" {
		return "#/"
	}
	return "#/room/" + url.PathEscape(roomID)
}
