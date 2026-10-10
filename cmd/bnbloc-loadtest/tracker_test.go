package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sse"
)

func TestTrackerMatchesFirstArrivalPerExpectedStream(t *testing.T) {
	tr := newTracker()
	t0 := time.Unix(1000, 0)
	tr.begin("m1", t0, []int{1, 2, 3})
	tr.commit("m1")
	tr.deliver(1, "m1", t0.Add(10*time.Millisecond))
	tr.deliver(2, "m1", t0.Add(20*time.Millisecond))
	tr.deliver(2, "m1", t0.Add(25*time.Millisecond)) // repeat (replay after reconnect)
	tr.deliver(9, "m1", t0.Add(30*time.Millisecond)) // stream opened after the send
	tr.deliver(1, "other", t0)                       // not ours

	if got := tr.outstanding(); got != 1 {
		t.Fatalf("outstanding = %d, want 1 (stream 3)", got)
	}
	tr.deliver(3, "m1", t0.Add(40*time.Millisecond))
	tr.deliver(3, "m1", t0.Add(50*time.Millisecond)) // after completion: extra

	res := tr.result()
	if res.expected != 3 || res.delivered != 3 || res.extra != 3 {
		t.Fatalf("expected/delivered/extra = %d/%d/%d, want 3/3/3", res.expected, res.delivered, res.extra)
	}
	want := msDurations(10, 20, 40)
	for i, d := range want {
		if res.latencies[i] != d {
			t.Fatalf("latencies = %v, want %v", res.latencies, want)
		}
	}
}

func TestTrackerEchoBeforePostAnswer(t *testing.T) {
	// The echo can beat the POST answer: it is held, then counted on commit.
	tr := newTracker()
	t0 := time.Unix(1000, 0)
	tr.begin("m1", t0, []int{1, 2})
	tr.deliver(1, "m1", t0.Add(5*time.Millisecond))
	if res := tr.result(); res.delivered != 0 || res.expected != 0 {
		t.Fatalf("uncommitted send counted: %+v", res)
	}
	tr.commit("m1")
	tr.deliver(2, "m1", t0.Add(7*time.Millisecond))
	res := tr.result()
	if res.expected != 2 || res.delivered != 2 || len(res.latencies) != 2 {
		t.Fatalf("after commit: %+v", res)
	}
	if res.latencies[0] != 5*time.Millisecond {
		t.Fatalf("early latency = %v, want 5ms", res.latencies[0])
	}
}

func TestTrackerAbortedSend(t *testing.T) {
	tr := newTracker()
	t0 := time.Unix(1000, 0)
	tr.begin("m1", t0, []int{1})
	tr.deliver(1, "m1", t0.Add(time.Millisecond)) // the server stored it, the answer was lost
	tr.abort("m1")
	tr.deliver(1, "m1", t0.Add(2*time.Millisecond))
	res := tr.result()
	if res.expected != 0 || res.delivered != 0 || res.extra != 1 {
		t.Fatalf("aborted send: %+v, want 0 expected, 0 delivered, 1 extra", res)
	}
}

func TestTrackerLostDeliveries(t *testing.T) {
	tr := newTracker()
	t0 := time.Unix(1000, 0)
	tr.begin("m1", t0, []int{1, 2})
	tr.commit("m1")
	tr.deliver(1, "m1", t0.Add(time.Millisecond))
	tr.begin("m2", t0, nil) // nobody was open: nothing expected
	tr.commit("m2")
	res := tr.result()
	if lost := res.expected - res.delivered; lost != 1 {
		t.Fatalf("lost = %d, want 1", lost)
	}
}

func TestDecodeStreamEvents(t *testing.T) {
	body := ": connected\n\n" +
		"id: 7\nevent: presence_join\ndata: {\"sequence\":7,\"kind\":\"presence_join\",\"payload\":{\"userId\":\"maya\"}}\n\n" +
		": keep-alive\n\n" +
		"id: 8\r\nevent: chat_message\r\ndata: {\"sequence\":8,\"kind\":\"chat_message\",\r\ndata: \"payload\":{\"userId\":\"jack\",\"clientMsgId\":\"lt-1\"}}\r\n\r\n" +
		"data: not json\n\n" +
		"event: chat_message\ndata: {\"sequence\":9,\"payload\":{\"clientMsgId\":\"lt-2\"}}\n\n"
	var got []roomEvent
	var kinds []string
	rd := sse.NewReader(strings.NewReader(body))
	for {
		raw, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		kinds = append(kinds, raw.Name)
		if ev, ok := decodeEvent(raw.Name, raw.Data); ok {
			got = append(got, ev)
		}
	}
	if strings.Join(kinds, ",") != "presence_join,chat_message,,chat_message" {
		t.Fatalf("kinds = %v", kinds)
	}
	if len(got) != 3 {
		t.Fatalf("decoded %d events, want 3: %+v", len(got), got)
	}
	if got[0].clientMsgID != "" || got[0].Sequence != 7 {
		t.Errorf("presence event decoded as %+v", got[0])
	}
	// Multi-line data joins with \n; CRLF line ends are accepted.
	if got[1].clientMsgID != "lt-1" || got[1].Sequence != 8 {
		t.Errorf("chat event decoded as %+v", got[1])
	}
	// The SSE event name supplies the kind when the JSON lacks it.
	if got[2].clientMsgID != "lt-2" {
		t.Errorf("kind from event name: %+v", got[2])
	}
}

func TestSessionClaims(t *testing.T) {
	tok := fakeToken("maya", time.Unix(2000000000, 0))
	sub, exp, err := sessionClaims(tok)
	if err != nil || sub != "maya" || exp.Unix() != 2000000000 {
		t.Fatalf("sessionClaims = %q, %v, %v", sub, exp, err)
	}
	for _, bad := range []string{"nodot", "!!!.sig", fakeKindToken("refresh")} {
		if _, _, err := sessionClaims(bad); err == nil {
			t.Errorf("sessionClaims(%q) accepted", bad)
		}
	}
}

func TestAssignRoomsPartitionsAndWraps(t *testing.T) {
	ms := make([]*member, 6)
	for i := range ms {
		ms[i] = &member{subject: string(rune('a' + i))}
	}
	rooms, per := assignRooms(config{Rooms: 3}, ms, "x")
	if per != 2 {
		t.Fatalf("per room = %d, want 2", per)
	}
	seen := map[string]int{}
	for _, r := range rooms {
		for _, m := range r.members {
			seen[m.subject]++
		}
	}
	if len(seen) != 6 {
		t.Fatalf("partition covers %d members, want 6", len(seen))
	}
	if rate := perMemberRate(6, rooms); rate != 1 {
		t.Fatalf("per member rate = %v, want 1", rate)
	}
	// 3 rooms x 4 members over 6 members: everyone is in two rooms.
	rooms, _ = assignRooms(config{Rooms: 3, MembersPerRoom: 4}, ms, "x")
	seen = map[string]int{}
	for _, r := range rooms {
		for _, m := range r.members {
			seen[m.subject]++
		}
	}
	for s, n := range seen {
		if n != 2 {
			t.Fatalf("member %s in %d rooms, want 2", s, n)
		}
	}
	if rate := perMemberRate(12, rooms); rate != 2 {
		t.Fatalf("per member rate = %v, want 2", rate)
	}
}
