package heartbeat

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestPacketRoundTripAndAuth(t *testing.T) {
	b, err := packet{Seq: 7, TS: 123, Name: "tk-site-web"}.encode([]byte("k"))
	if err != nil || len(b) != PacketSz {
		t.Fatalf("encode: %v len=%d", err, len(b))
	}
	p, err := decode([]byte("k"), b)
	if err != nil || p.Seq != 7 || p.TS != 123 || p.Name != "tk-site-web" || p.Reply {
		t.Fatalf("decode: %+v %v", p, err)
	}
	if _, err := decode([]byte("other"), b); err == nil {
		t.Fatal("wrong key must not verify")
	}
	b[20] ^= 1
	if _, err := decode([]byte("k"), b); err == nil {
		t.Fatal("tampered packet must not verify")
	}
	if _, err := (packet{Name: "bad name!"}).encode(nil); err == nil {
		t.Fatal("invalid name accepted")
	}
}

type rep struct {
	mu   sync.Mutex
	ok   bool
	rtt  time.Duration
	n    int
	loss float64
}

func (r *rep) SetUDP(ok bool, rtt time.Duration, loss float64) {
	r.mu.Lock()
	r.ok, r.rtt, r.loss, r.n = ok, rtt, loss, r.n+1
	r.mu.Unlock()
}

func freeUDP(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := pc.LocalAddr().String()
	pc.Close()
	return a
}

func TestEndToEnd(t *testing.T) {
	addr := freeUDP(t)
	s := NewServer("secret")
	go s.Serve(addr)
	time.Sleep(100 * time.Millisecond)

	host, port, _ := net.SplitHostPort(addr)
	pn, _ := net.LookupPort("udp", port)
	r := &rep{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunClient(ctx, host, pn, "tk-x-web", "secret", 50*time.Millisecond, r)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n, ok := r.n, r.ok
		r.mu.Unlock()
		if n >= 3 && ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	if r.n < 3 || !r.ok || r.loss != 0 {
		t.Fatalf("client did not see replies: %+v", r)
	}
	r.mu.Unlock()
	if at, ip, ok := s.Last("tk-x-web"); !ok || ip != "127.0.0.1" || time.Since(at) > time.Second {
		t.Fatalf("server did not record the heartbeat: %v %q %v", at, ip, ok)
	}
}

func TestWrongKeyGetsNoReplyAndIsNotRecorded(t *testing.T) {
	addr := freeUDP(t)
	s := NewServer("secret")
	go s.Serve(addr)
	time.Sleep(100 * time.Millisecond)

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := packet{Seq: 1, Name: "tk-evil-web"}.encode([]byte("guess"))
	c.Write(b)
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if n, err := c.Read(make([]byte, 2048)); err == nil {
		t.Fatalf("got a %d-byte reply to an unauthenticated packet", n)
	}
	if _, _, ok := s.Last("tk-evil-web"); ok {
		t.Fatal("unauthenticated heartbeat was recorded")
	}
}

func TestLostAndResumeCallbacks(t *testing.T) {
	s := NewServer("")
	var mu sync.Mutex
	var events []string
	s.OnLost = func(n, _ string, _ time.Duration) { mu.Lock(); events = append(events, "lost"); mu.Unlock() }
	s.OnResume = func(n, _ string, _ time.Duration) { mu.Lock(); events = append(events, "resume"); mu.Unlock() }
	a := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1}
	s.note("tk-a-web", a)
	s.mu.Lock()
	s.m["tk-a-web"].alive = false // as the checker does after LostAfter
	s.mu.Unlock()
	s.note("tk-a-web", a)
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0] != "resume" {
		t.Fatalf("events=%v", events)
	}
}
