package heartbeat

import (
	"context"
	"log"
	"net"
	"strconv"
	"time"
)

// Reporter receives the client's view of the UDP path (stats.Stats satisfies it).
type Reporter interface {
	SetUDP(ok bool, rtt time.Duration, lossPct float64)
}

const (
	// missedBeforeWarn consecutive unanswered beats before the client says so.
	missedBeforeWarn = 3
	// window is how many recent beats the loss percentage covers.
	window = 30
	// replyWait is how long a beat may take to come back (satellite RTT ~0.6 s).
	replyWait = 5 * time.Second
)

type reply struct {
	seq uint32
	at  time.Time
}

// RunClient sends a heartbeat every interval to host:port and tracks the
// replies. rep may be nil. It blocks until ctx is done.
func RunClient(ctx context.Context, host string, port int, name, key string, interval time.Duration, rep Reporter) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if !validName(name) {
		log.Printf("heartbeat: client name %q is not valid, UDP heartbeat disabled", name)
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	k := []byte(key)
	replies := make(chan reply, 16)

	var conn net.Conn
	dial := func() {
		if conn != nil {
			conn.Close()
			conn = nil
		}
		c, err := net.Dial("udp", addr)
		if err != nil {
			log.Printf("heartbeat: %v", err)
			return
		}
		conn = c
		go func() {
			buf := make([]byte, 2048)
			for {
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				if p, err := decode(k, buf[:n]); err == nil && p.Reply && p.Name == name {
					select {
					case replies <- reply{p.Seq, time.Now()}:
					default:
					}
				}
			}
		}()
	}
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()

	var (
		seq      uint32
		sent     = map[uint32]time.Time{}
		hist     []bool // outcome of the last beats, newest last (true = answered)
		missed   int
		downFrom time.Time
		lastRTT  time.Duration
	)
	record := func(ok bool) {
		hist = append(hist, ok)
		if len(hist) > window {
			hist = hist[len(hist)-window:]
		}
	}
	loss := func() float64 {
		lost := 0
		for _, h := range hist {
			if !h {
				lost++
			}
		}
		return 100 * float64(lost) / float64(len(hist))
	}

	for {
		if conn == nil {
			dial()
		}
		if conn != nil {
			seq++
			now := time.Now()
			if b, err := (packet{Seq: seq, TS: now.UnixNano(), Name: name}).encode(k); err == nil {
				sent[seq] = now
				if _, err := conn.Write(b); err != nil {
					log.Printf("heartbeat: send: %v", err)
					conn.Close()
					conn = nil // re-dial (and re-resolve) on the next beat
				}
			}
		}
		deadline := time.NewTimer(interval)
	wait:
		for {
			select {
			case <-ctx.Done():
				deadline.Stop()
				return
			case r := <-replies:
				t0, ok := sent[r.seq]
				if !ok {
					continue // late reply to a beat already counted as lost
				}
				delete(sent, r.seq)
				lastRTT = r.at.Sub(t0)
				record(true)
				if missed >= missedBeforeWarn {
					log.Printf("heartbeat: UDP replies again after %s (rtt %s)",
						time.Since(downFrom).Round(time.Second), lastRTT.Round(time.Millisecond))
				}
				missed = 0
				if rep != nil {
					rep.SetUDP(true, lastRTT, loss())
				}
			case <-deadline.C:
				break wait
			}
		}
		// Beats older than replyWait that never came back are lost.
		for s, at := range sent {
			if time.Since(at) >= replyWait && s != seq {
				delete(sent, s)
				record(false)
				if missed++; missed == missedBeforeWarn {
					downFrom = at
					log.Printf("heartbeat: %d UDP beats to %s without reply", missed, addr)
				}
				if rep != nil {
					rep.SetUDP(missed < missedBeforeWarn, lastRTT, loss())
				}
			}
		}
	}
}
