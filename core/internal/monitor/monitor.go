// Package monitor serves the tunnel server's web panel: connected clients
// (from the Registry that the tunnelserver fills), enriched with each client's
// /metrics, with history (traffic/connection sparklines), REAL traffic counted
// on the server, and a per-client speedtest button.
package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adriansanchis87/tunnelkit/internal/speedtest"
)

const histLen = 120 // samples kept (~10 min at 5s)

type sample struct {
	Ts      int64   `json:"ts"`
	TxRate  float64 `json:"tx"` // bytes/s
	RxRate  float64 `json:"rx"` // bytes/s
	Active  int64   `json:"active"`
	Latency float64 `json:"lat"` // ms
}

// Client is a connected client. The traffic/connection counters are incremented
// by the server as it pipes; the rest is filled in by the monitor's sampler.
type Client struct {
	Name  string
	IP    string
	Since time.Time

	tx, rx atomic.Uint64 // bytes accumulated over the tunnel
	active atomic.Int64  // active forwarded connections

	mu         sync.Mutex
	ports      []uint32
	reconnects int
	lastError  string
	speedPort  int
	hist       []sample
	prevTx     uint64
	prevRx     uint64
	prevAt     time.Time
	lastDown   float64
	lastUp     float64
	speedHist  []float64
}

// Registry is the shared state of connected clients (thread-safe).
type Registry struct {
	mu sync.Mutex
	m  map[string]*Client
}

func NewRegistry() *Registry { return &Registry{m: map[string]*Client{}} }

func (r *Registry) Add(id string, c *Client) { r.mu.Lock(); r.m[id] = c; r.mu.Unlock() }
func (r *Registry) Del(id string)            { r.mu.Lock(); delete(r.m, id); r.mu.Unlock() }
func (r *Registry) Get(id string) *Client    { r.mu.Lock(); defer r.mu.Unlock(); return r.m[id] }

func (r *Registry) AddPort(id string, port uint32) {
	r.mu.Lock()
	c := r.m[id]
	r.mu.Unlock()
	if c != nil {
		c.mu.Lock()
		c.ports = append(c.ports, port)
		c.mu.Unlock()
	}
}

func (r *Registry) list() []*Client {
	r.mu.Lock()
	out := make([]*Client, 0, len(r.m))
	for _, c := range r.m {
		out = append(out, c)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) byName(name string) *Client {
	for _, c := range r.list() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// --- counters for the server (pipe) ---

func (c *Client) AddTx(n int) { c.tx.Add(uint64(n)) }
func (c *Client) AddRx(n int) { c.rx.Add(uint64(n)) }
func (c *Client) ConnOpen()   { c.active.Add(1) }
func (c *Client) ConnClose()  { c.active.Add(-1) }

// --- sampler: scrapes /metrics and computes rates for the history ---

func (r *Registry) sample() {
	for _, c := range r.list() {
		c.scrape()
	}
}

type metrics struct {
	Reconnects    int    `json:"reconnects"`
	SpeedtestPort int    `json:"speedtest_port"`
	LastError     string `json:"last_error"`
}

func (c *Client) scrape() {
	c.mu.Lock()
	ports := append([]uint32(nil), c.ports...)
	c.mu.Unlock()

	// Latency = HTTP round-trip of reading the client's /metrics, which DOES
	// cross the tunnel (unlike connecting to the forwarded port, which the server
	// accepts locally). On satellite it hovers around the link RTT (~600ms) with
	// high jitter; the device's processing adds little.
	var m metrics
	var lat float64
	cl := &http.Client{Timeout: 5 * time.Second}
	for _, p := range ports {
		t := time.Now()
		resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", p))
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if json.Unmarshal(body, &m) == nil {
			lat = float64(time.Since(t).Microseconds()) / 1000
			break
		}
	}

	now := time.Now()
	tx, rx := c.tx.Load(), c.rx.Load()
	c.mu.Lock()
	c.reconnects, c.lastError, c.speedPort = m.Reconnects, m.LastError, m.SpeedtestPort
	var txr, rxr float64
	if !c.prevAt.IsZero() {
		if dt := now.Sub(c.prevAt).Seconds(); dt > 0 {
			txr = float64(tx-c.prevTx) / dt
			rxr = float64(rx-c.prevRx) / dt
		}
	}
	c.prevTx, c.prevRx, c.prevAt = tx, rx, now
	c.hist = append(c.hist, sample{now.Unix(), txr, rxr, c.active.Load(), lat})
	if len(c.hist) > histLen {
		c.hist = c.hist[len(c.hist)-histLen:]
	}
	c.mu.Unlock()
}

// --- API ---

type row struct {
	Name         string    `json:"name"`
	Connected    bool      `json:"connected"`
	IP           string    `json:"ip"`
	Ports        []uint32  `json:"ports"`
	Uptime       float64   `json:"uptime_seconds"`
	Reconnects   int       `json:"reconnects"`
	Latency      float64   `json:"latency_ms"`
	Active       int64     `json:"active"`
	BytesTx      uint64    `json:"bytes_tx"`
	BytesRx      uint64    `json:"bytes_rx"`
	LastError    string    `json:"last_error"`
	Hist         []sample  `json:"hist"`
	Speedtest    bool      `json:"speedtest"` // responder present
	LastDown     float64   `json:"last_down"`
	LastUp       float64   `json:"last_up"`
	SpeedHist    []float64 `json:"speed_hist"`
	TrafficToday uint64    `json:"traffic_today"` // today's bytes (tx+rx)
}

func (c *Client) row() row {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lat float64
	if n := len(c.hist); n > 0 {
		lat = c.hist[n-1].Latency
	}
	return row{
		Name: c.Name, Connected: true, IP: c.IP,
		Ports:  append([]uint32(nil), c.ports...),
		Uptime: time.Since(c.Since).Seconds(), Reconnects: c.reconnects,
		Latency: lat, Active: c.active.Load(),
		BytesTx: c.tx.Load(), BytesRx: c.rx.Load(), LastError: c.lastError,
		Hist:      append([]sample(nil), c.hist...),
		Speedtest: c.speedPort > 0, LastDown: c.lastDown, LastUp: c.lastUp,
		SpeedHist: append([]float64(nil), c.speedHist...),
	}
}

// Serve starts the web panel (blocking) + the background sampler.
func Serve(addr string, reg *Registry, store *TrafficStore) error {
	go func() {
		for range time.Tick(5 * time.Second) {
			reg.sample()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		var rows []row
		for _, c := range reg.list() {
			r := c.row()
			if store != nil {
				r.TrafficToday = store.todayTotal(c.Name)
			}
			rows = append(rows, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"generated_at": time.Now().Unix(), "clients": rows,
		})
	})
	mux.HandleFunc("/api/traffic", func(w http.ResponseWriter, req *http.Request) {
		if store == nil {
			http.Error(w, "no store", http.StatusNotFound)
			return
		}
		ports, days := store.breakdown(req.URL.Query().Get("client"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ports": ports, "days": days})
	})
	mux.HandleFunc("/api/speedtest", func(w http.ResponseWriter, req *http.Request) {
		c := reg.byName(req.URL.Query().Get("client"))
		if c == nil {
			http.Error(w, "client not found", http.StatusNotFound)
			return
		}
		c.mu.Lock()
		port := c.speedPort
		c.mu.Unlock()
		if port == 0 {
			http.Error(w, "client does not expose speedtest", http.StatusBadRequest)
			return
		}
		// Duration-based measurement (~4s per direction): real on fast and slow links.
		down, up, err := speedtest.Measure(fmt.Sprintf("127.0.0.1:%d", port), 4*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		c.mu.Lock()
		c.lastDown, c.lastUp = down, up
		c.speedHist = append(c.speedHist, down)
		if len(c.speedHist) > 30 {
			c.speedHist = c.speedHist[len(c.speedHist)-30:]
		}
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]float64{"down": down, "up": up})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	return http.ListenAndServe(addr, mux)
}
