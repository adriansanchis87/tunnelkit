// Package stats exposes the client's tunnel state over HTTP (/metrics), meant
// for the ingress monitor to scrape it through a port forwarded by the tunnel
// itself.
package stats

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// Stats accumulates the tunnel session state. Concurrency-safe.
type Stats struct {
	mu         sync.Mutex
	startedAt  time.Time
	connected  bool
	sessionAt  time.Time // start of the current session
	reconnects int
	lastError  string
	lastUptime float64 // duration of the last session (s)

	// The real traffic is counted on the SERVER (which pipes the whole tunnel);
	// here they stay at 0. The speedtest port is published for the panel.
	// UDP heartbeat (see package heartbeat); udpSet false = not configured.
	udpSet  bool
	udpOK   bool
	udpRTT  time.Duration
	udpLoss float64
	udpAt   time.Time

	bytesTx       uint64
	bytesRx       uint64
	speedtestPort int
}

// New creates a Stats with the clock started. speedtestPort is the responder's
// remote forwarded speedtest port (0 = none), which is published in /metrics so
// the server's panel knows where to measure.
func New(speedtestPort int) *Stats {
	return &Stats{startedAt: time.Now(), speedtestPort: speedtestPort}
}

// SetConnected marks the session as established.
func (s *Stats) SetConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.sessionAt = time.Now()
}

// SetConnectedSince marks the session as established at t (the moment ssh was
// started, once it has stayed up long enough to be considered connected).
func (s *Stats) SetConnectedSince(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.sessionAt = t
}

// RecordDisconnect records the end of a session (reconnection).
func (s *Stats) RecordDisconnect(d time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	s.reconnects++
	s.lastUptime = d.Seconds()
	if err != nil {
		s.lastError = err.Error()
	}
}

// SetUDP records the latest state of the UDP heartbeat path.
func (s *Stats) SetUDP(ok bool, rtt time.Duration, lossPct float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.udpSet, s.udpOK, s.udpRTT, s.udpLoss, s.udpAt = true, ok, rtt, lossPct, time.Now()
}

type snapshot struct {
	Connected     bool    `json:"connected"`
	UptimeSeconds float64 `json:"uptime_seconds"` // current session
	AgentSeconds  float64 `json:"agent_seconds"`  // since the agent started
	Reconnects    int     `json:"reconnects"`
	LastUptimeSec float64 `json:"last_uptime_seconds"`
	LastError     string  `json:"last_error"`
	BytesTx       uint64  `json:"bytes_tx"`
	BytesRx       uint64  `json:"bytes_rx"`
	SpeedtestPort int     `json:"speedtest_port"`
	// Only present when the UDP heartbeat is configured.
	UDPOK      *bool    `json:"udp_ok,omitempty"`
	UDPRTTMs   *float64 `json:"udp_rtt_ms,omitempty"`
	UDPLossPct *float64 `json:"udp_loss_pct,omitempty"`
}

func (s *Stats) snap() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var up float64
	if s.connected {
		up = time.Since(s.sessionAt).Seconds()
	}
	sn := snapshot{
		Connected:     s.connected,
		UptimeSeconds: up,
		AgentSeconds:  time.Since(s.startedAt).Seconds(),
		Reconnects:    s.reconnects,
		LastUptimeSec: s.lastUptime,
		LastError:     s.lastError,
		BytesTx:       s.bytesTx,
		BytesRx:       s.bytesRx,
		SpeedtestPort: s.speedtestPort,
	}
	if s.udpSet {
		ok, rtt, loss := s.udpOK, float64(s.udpRTT.Microseconds())/1000, s.udpLoss
		sn.UDPOK, sn.UDPRTTMs, sn.UDPLossPct = &ok, &rtt, &loss
	}
	return sn
}

// Serve starts the metrics HTTP server (blocking).
func (s *Stats) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.snap())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if s.snap().Connected {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	log.Printf("stats: serving /metrics on %s", addr)
	return http.ListenAndServe(addr, mux)
}
