package roomlog

import (
	"context"
	"strings"
	"testing"
)

// The agent channel listing is read with the turn's job (?job=): the
// gateway reads it as the job's requester, so it names only the channels
// that person may read (channels PRD C14). Without a job, no grant is sent.
// A read of one conversation names it (?thread=), so a thread (never
// listed among the room's channels) is listed when it is the one read.
func TestAgentChannelListCarriesTheJob(t *testing.T) {
	var paths []string
	fetch := func(_ context.Context, path string) ([]byte, error) {
		paths = append(paths, path)
		return []byte(`{"channels":[{"channelId":"ch_general"}]}`), nil
	}
	AgentChannelList(context.Background(), fetch, "room 1", "job_1", "")
	AgentChannelList(context.Background(), fetch, "room-2", "", "")
	if _, _, err := History(context.Background(), fetch, "room-3", HistoryQuery{Only: "ch_general", Job: "job_2", Pages: 1}); err != nil {
		t.Fatal(err)
	}
	if len(paths) < 3 || paths[0] != "/v1/rooms/room%201/channels?agents=1&job=job_1" || paths[1] != "/v1/rooms/room-2/channels?agents=1" ||
		paths[2] != "/v1/rooms/room-3/channels?agents=1&job=job_2&thread=ch_general" {
		t.Fatalf("listing paths = %v", paths)
	}
}

// An agent answering in a thread reads the thread with its root first and
// not the parent's other messages (channels PRD §9: "sees the root and the
// thread, not the parent's later messages except as its file"); the
// listing names the thread under its channel.
func TestThreadRecordsHoldTheRootAndTheThread(t *testing.T) {
	fetch := func(_ context.Context, path string) ([]byte, error) {
		switch {
		case strings.Contains(path, "/channels?"):
			if !strings.Contains(path, "thread=th_1") {
				return []byte(`{"channels":[{"channelId":"ch_rel","name":"releases"}]}`), nil
			}
			return []byte(`{"channels":[{"channelId":"ch_rel","name":"releases"},
				{"channelId":"th_1","name":"login bug","kind":"thread","thread":{"parentId":"ch_rel","rootSeq":5,"replies":2,"participants":[]}}]}`), nil
		case strings.Contains(path, "channelId=th_1"):
			return []byte(`{"messages":[
				{"sequence":8,"kind":"chat_message","payload":{"userId":"jack","text":"I can reproduce it"}},
				{"sequence":9,"kind":"chat_message","payload":{"userId":"maya","text":"which build?"}}]}`), nil
		case strings.Contains(path, "channelId=ch_rel") && strings.Contains(path, "before=6"):
			return []byte(`{"messages":[{"sequence":5,"kind":"chat_message","payload":{"userId":"maya","text":"the login bug is back"}}]}`), nil
		case strings.Contains(path, "channelId=ch_rel"):
			return []byte(`{"messages":[
				{"sequence":5,"kind":"chat_message","payload":{"userId":"maya","text":"the login bug is back"}},
				{"sequence":10,"kind":"chat_message","payload":{"userId":"lee","text":"the parent moved on"}}]}`), nil
		}
		return []byte(`{}`), nil
	}
	list, ok := AgentChannelList(context.Background(), fetch, "r1", "job", "th_1")
	th, found := ChannelOf(list, "th_1")
	if !ok || !found || th.Name != "releases › login bug" || th.ParentID != "ch_rel" || th.RootSeq != 5 {
		t.Fatalf("thread in the listing = %+v (%v %v)", th, ok, found)
	}
	entries, _, err := History(context.Background(), fetch, "r1", HistoryQuery{Only: "th_1", Job: "job"})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range entries {
		texts = append(texts, e.Text)
		if e.ChannelID != "th_1" {
			t.Fatalf("an entry of the thread's records is %s's", e.ChannelID)
		}
	}
	if strings.Join(texts, "|") != "the login bug is back|I can reproduce it|which build?" {
		t.Fatalf("the thread's records = %v", texts)
	}
	// A delta past the root (a later turn) does not repeat it.
	entries, _, _ = History(context.Background(), fetch, "r1", HistoryQuery{Only: "th_1", Job: "job", After: 8})
	for _, e := range entries {
		if e.Seq == 5 {
			t.Fatal("a delta after the root repeats it")
		}
	}
}
