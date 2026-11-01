package heartbeat

import (
	"log"
	"net"
	"sync"
	"time"
)

// LostAfter is how long without a heartbeat before the server calls a client's
// UDP path lost (3 missed beats at the default interval, plus slack).
const LostAfter = 35 * time.Second

type seen struct {
	at    time.Time
	ip    string
	alive bool
}

// Server answers heartbeats and remembers the last one per client name.
type Server struct {
	key []byte

	// OnLost / OnResume are called (from the checker goroutine and the receive
	// loop) when a client's heartbeats stop for LostAfter / start again.
	OnLost   func(name, ip string, lastAgo time.Duration)
	OnResume func(name, ip string, gap time.Duration)

	mu sync.Mutex
	m  map[string]*seen
}

func NewServer(key string) *Server {
	return &Server{key: []byte(key), m: map[string]*seen{}}
}

// Last returns when and from which IP the client's last valid heartbeat came.
func (s *Server) Last(name string) (at time.Time, ip string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.m[name]; v != nil {
		return v.at, v.ip, true
	}
	return time.Time{}, "", false
}

// Serve listens on addr (UDP) and blocks.
func (s *Server) Serve(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	go s.check()
	log.Printf("heartbeat: UDP echo on %s", addr)
	buf := make([]byte, 2048)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		p, err := decode(s.key, buf[:n])
		if err != nil || p.Reply {
			continue // not ours / not authentic: silent
		}
		s.note(p.Name, from)
		rep, err := packet{Reply: true, Seq: p.Seq, TS: p.TS, Name: p.Name}.encode(s.key)
		if err == nil {
			_, _ = pc.WriteTo(rep, from)
		}
	}
}

func hostOf(a net.Addr) string {
	if h, _, err := net.SplitHostPort(a.String()); err == nil {
		return h
	}
	return a.String()
}

func (s *Server) note(name string, from net.Addr) {
	now := time.Now()
	ip := hostOf(from)
	s.mu.Lock()
	v := s.m[name]
	if v == nil {
		v = &seen{alive: true}
		s.m[name] = v
		s.mu.Unlock()
		log.Printf("heartbeat: first UDP heartbeat from %s (%s)", name, ip)
		s.mu.Lock()
	}
	resumed := !v.alive
	gap := now.Sub(v.at)
	v.at, v.ip, v.alive = now, ip, true
	s.mu.Unlock()
	if resumed && s.OnResume != nil {
		s.OnResume(name, ip, gap)
	}
}

// check flags clients whose heartbeats stopped.
func (s *Server) check() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		type ev struct {
			name, ip string
			ago      time.Duration
		}
		var lost []ev
		s.mu.Lock()
		for n, v := range s.m {
			if v.alive && time.Since(v.at) >= LostAfter {
				v.alive = false
				lost = append(lost, ev{n, v.ip, time.Since(v.at)})
			}
		}
		s.mu.Unlock()
		if s.OnLost != nil {
			for _, e := range lost {
				s.OnLost(e.name, e.ip, e.ago)
			}
		}
	}
}
