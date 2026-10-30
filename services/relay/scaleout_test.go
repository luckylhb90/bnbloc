package relay

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// memoryMeet is the rendezvous in one process.
type memoryMeet struct {
	mu sync.Mutex
	at map[string]string
}

func (m *memoryMeet) Meet(key, holder string, _ time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at, ok := m.at[key]; ok {
		return at, nil
	}
	m.at[key] = holder
	return holder, nil
}

func (m *memoryMeet) Leave(key, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at[key] == holder {
		delete(m.at, key)
	}
	return nil
}

func (m *memoryMeet) records() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.at)
}

// relayCluster starts two relay replicas sharing one rendezvous.
func relayCluster(t *testing.T) ([]*httptest.Server, *memoryMeet) {
	t.Helper()
	meet := &memoryMeet{at: map[string]string{}}
	srvs := make([]*httptest.Server, 2)
	for i := range srvs {
		srv := httptest.NewUnstartedServer(nil)
		svc := New()
		svc.Meet, svc.Self = meet, srv.Listener.Addr().String()
		srv.Config.Handler = svc.Handler()
		srv.Start()
		t.Cleanup(srv.Close)
		srvs[i] = srv
	}
	return srvs, meet
}

func exchange(t *testing.T, first, second interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	SetReadDeadline(time.Time) error
}) {
	t.Helper()
	if _, err := first.Write([]byte("hello-across-replicas")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := second.Read(buf)
	if err != nil || string(buf[:n]) != "hello-across-replicas" {
		t.Fatalf("second read %q, %v", buf[:n], err)
	}
	if _, err := second.Write([]byte("and-back")); err != nil {
		t.Fatal(err)
	}
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err = first.Read(buf); err != nil || string(buf[:n]) != "and-back" {
		t.Fatalf("first read %q, %v", buf[:n], err)
	}
}

// Two peers of one session that reach different replicas still pair: the
// second finds the first's replica at the rendezvous and joins it there;
// the record is gone once they paired.
func TestRelayPairsAcrossReplicas(t *testing.T) {
	srvs, meet := relayCluster(t)
	first, err := wsconn.Dial(srvs[0].URL+"/relay/stream/sess-1", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	deadline := time.Now().Add(2 * time.Second)
	for meet.records() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first half never recorded its replica")
		}
		time.Sleep(5 * time.Millisecond)
	}
	second, err := wsconn.Dial(srvs[1].URL+"/relay/stream/sess-1", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	exchange(t, first, second)
	if meet.records() != 0 {
		t.Fatal("the rendezvous record outlived the pairing")
	}
}

// Both halves on one replica pair there, as with a single replica.
func TestRelayPairsOnOneReplica(t *testing.T) {
	srvs, meet := relayCluster(t)
	first, err := wsconn.Dial(srvs[1].URL+"/relay/stream/sess-2", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for meet.records() != 1 {
		time.Sleep(5 * time.Millisecond)
	}
	second, err := wsconn.Dial(srvs[1].URL+"/relay/stream/sess-2", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	exchange(t, first, second)
}

// A record left by a replica that died is cleared, and the half parks
// where it landed for its partner.
func TestRelayClearsADeadReplicasRecord(t *testing.T) {
	srvs, meet := relayCluster(t)
	_, _ = meet.Meet("sess-3", "127.0.0.1:1 deadtoken", time.Minute)
	first, err := wsconn.Dial(srvs[0].URL+"/relay/stream/sess-3", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	deadline := time.Now().Add(7 * time.Second)
	for {
		meet.mu.Lock()
		at := meet.at["sess-3"]
		meet.mu.Unlock()
		if strings.HasPrefix(at, strings.TrimPrefix(srvs[0].URL, "http://")+" ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("record %q, want replica 0's", at)
		}
		time.Sleep(10 * time.Millisecond)
	}
	second, err := wsconn.Dial(srvs[1].URL+"/relay/stream/sess-3", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	exchange(t, first, second)
}

// A join without the parked half's token is refused.
func TestRelayJoinNeedsTheToken(t *testing.T) {
	srvs, meet := relayCluster(t)
	first, err := wsconn.Dial(srvs[0].URL+"/relay/stream/sess-4", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for meet.records() != 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if c, err := wsconn.Dial(srvs[0].URL+"/internal/relay/join/sess-4?t=guess", 2*time.Second); err == nil {
		c.Close()
		t.Fatal("a join with a wrong token was accepted")
	}
}
