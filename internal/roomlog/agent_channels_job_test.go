package roomlog

import (
	"context"
	"testing"
)

// The agent channel listing is read with the turn's job (?job=): the
// gateway reads it as the job's requester, so it names only the channels
// that person may read (channels PRD C14). Without a job, no grant is sent.
func TestAgentChannelListCarriesTheJob(t *testing.T) {
	var paths []string
	fetch := func(_ context.Context, path string) ([]byte, error) {
		paths = append(paths, path)
		return []byte(`{"channels":[{"channelId":"ch_general"}]}`), nil
	}
	AgentChannelList(context.Background(), fetch, "room 1", "job_1")
	AgentChannelList(context.Background(), fetch, "room-2", "")
	if _, _, err := History(context.Background(), fetch, "room-3", HistoryQuery{Only: "ch_general", Job: "job_2", Pages: 1}); err != nil {
		t.Fatal(err)
	}
	if len(paths) < 3 || paths[0] != "/v1/rooms/room%201/channels?agents=1&job=job_1" || paths[1] != "/v1/rooms/room-2/channels?agents=1" ||
		paths[2] != "/v1/rooms/room-3/channels?agents=1&job=job_2" {
		t.Fatalf("listing paths = %v", paths)
	}
}
