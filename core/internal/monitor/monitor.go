// Package monitor serves the tunnel server's web panel: connected clients
// (from the Registry that the tunnelserver fills), enriched with each client's
// /metrics, with history (traffic/connection sparklines), REAL traffic counted
// on the server, and a per-client speedtest button.
package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
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
	Rec     int     `json:"rec"` // cumulative reconnects at this sample (for the drop graph)
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
	c.hist = append(c.hist, sample{now.Unix(), txr, rxr, c.active.Load(), lat, c.reconnects})
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
	// OfflineSeconds is how long a KNOWN client has been disconnected. It is
	// only set on rows where Connected is false (built from the traffic store's
	// last-seen record); 0 on connected clients.
	OfflineSeconds float64 `json:"offline_seconds"`
	// Uptime24h is the % of the last 24h the client was connected (from the
	// availability log); -1 when there is no data.
	Uptime24h float64 `json:"uptime_24h"`
	// Links are the web entry points the panel offers for this client, built
	// from the operator's LinkConfig (empty when no template matches).
	Links []link `json:"links,omitempty"`
}

// link is one web entry point of a client. Kind is "main" (the client's own
// service) or "backup" (the sibling client of the same site, reached through
// this one). Host is the subdomain label only: the panel prepends the scheme
// and appends the panel's own parent domain.
type link struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Host  string `json:"host"`
}

// LinkConfig turns client names of the form "tk-<site>-<role>" into subdomain
// labels. It is entirely operator-provided (TK_SERVER_LINKS /
// TK_SERVER_LINK_BACKUP_SUFFIX): nothing about a particular deployment's
// naming lives in the code.
type LinkConfig struct {
	Templates    map[string]string // role -> template with {site} and {role}
	BackupSuffix string
}

// ParseLinkConfig parses "role=template,role=template" (spaces tolerated).
func ParseLinkConfig(spec, backupSuffix string) LinkConfig {
	lc := LinkConfig{Templates: map[string]string{}, BackupSuffix: backupSuffix}
	for _, kv := range strings.Split(spec, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		if i := strings.IndexByte(kv, '='); i > 0 {
			lc.Templates[strings.ToLower(strings.TrimSpace(kv[:i]))] = strings.TrimSpace(kv[i+1:])
		}
	}
	return lc
}

// splitName parses "tk-<site>-<role>" into (site, role), lowercase and
// alphanumeric only; ok=false for any other name.
func splitName(name string) (site, role string, ok bool) {
	name = strings.ToLower(name)
	if !strings.HasPrefix(name, "tk-") {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, "tk-")
	i := strings.LastIndex(rest, "-")
	if i < 0 {
		return "", "", false
	}
	site, role = onlyAlnum(rest[:i]), onlyAlnum(rest[i+1:])
	return site, role, site != "" && role != ""
}

// mainHost is the subdomain label of a client's main service, or "" when the
// operator configured no template for its role.
func (lc LinkConfig) mainHost(name string) string {
	site, role, ok := splitName(name)
	if !ok {
		return ""
	}
	t, ok := lc.Templates[role]
	if !ok {
		return ""
	}
	return strings.NewReplacer("{site}", site, "{role}", role).Replace(t)
}

// linksFor builds the panel links of a client: its main service and, if a
// sibling of the same site with another role is known, a backup link to that
// sibling through this client (<sibling main label><BackupSuffix>).
func (lc LinkConfig) linksFor(name string, known []string) []link {
	main := lc.mainHost(name)
	if main == "" {
		return nil
	}
	_, role, _ := splitName(name)
	out := []link{{Kind: "main", Label: role, Host: main}}
	if lc.BackupSuffix == "" {
		return out
	}
	site, _, _ := splitName(name)
	for _, other := range known {
		os_, orole, ok := splitName(other)
		if !ok || os_ != site || orole == role {
			continue
		}
		if h := lc.mainHost(other); h != "" {
			out = append(out, link{Kind: "backup", Label: orole + " via " + role, Host: h + lc.BackupSuffix})
		}
	}
	return out
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
func Serve(addr string, reg *Registry, store *TrafficStore, links LinkConfig) error {
	startAt := time.Now()
	go func() {
		for range time.Tick(5 * time.Second) {
			reg.sample()
			if store == nil {
				continue
			}
			// Record every currently-connected client as "seen now" so that,
			// once it drops, we can report exactly how long it has been offline.
			connected := map[string]bool{}
			for _, c := range reg.list() {
				if c.row().Connected {
					store.Touch(c.Name)
					connected[c.Name] = true
				}
			}
			// Availability log: mark each known client up/down (RecordState only
			// writes on a real change). During the first 30s after a server
			// (re)start we skip DOWN marks, so a client that reconnects quickly
			// after a deploy stays continuously up instead of getting a blip that
			// is really the server's own downtime.
			grace := time.Since(startAt) < 30*time.Second
			for name := range store.Seen() {
				up := connected[name]
				if !up && grace {
					continue
				}
				store.RecordState(name, up)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		var rows []row
		connected := map[string]bool{}
		// Every client name we know, connected or not (for backup links).
		knownSet := map[string]bool{}
		for _, c := range reg.list() {
			knownSet[c.Name] = true
		}
		if store != nil {
			for name := range store.Seen() {
				knownSet[name] = true
			}
		}
		known := make([]string, 0, len(knownSet))
		for name := range knownSet {
			known = append(known, name)
		}
		sort.Strings(known)
		for _, c := range reg.list() {
			r := c.row()
			r.Uptime24h = -1
			if store != nil {
				r.TrafficToday = store.todayTotal(c.Name)
				r.Uptime24h = store.uptimePct(c.Name, 86400)
			}
			r.Links = links.linksFor(c.Name, known)
			rows = append(rows, r)
			connected[c.Name] = true
		}
		// Append known-but-currently-disconnected clients so the panel can show
		// them as offline together with how long they have been down.
		if store != nil {
			now := time.Now().Unix()
			for name, lastSeen := range store.Seen() {
				if connected[name] {
					continue
				}
				off := float64(now - lastSeen)
				if off < 0 {
					off = 0
				}
				rows = append(rows, row{
					Name:           name,
					Connected:      false,
					TrafficToday:   store.todayTotal(name),
					OfflineSeconds: off,
					Uptime24h:      store.uptimePct(name, 86400),
					Links:          links.linksFor(name, known),
				})
			}
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
	mux.HandleFunc("/api/uptime", func(w http.ResponseWriter, req *http.Request) {
		if store == nil {
			http.Error(w, "no store", http.StatusNotFound)
			return
		}
		name := req.URL.Query().Get("client")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": name, "now": time.Now().Unix(),
			"events": store.uptimeSeries(name),
		})
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

	// Root handler: if the request's host label equals a connected client's
	// main-service label (per LinkConfig), reverse-proxy to that client's
	// lowest forwarded port at the ROOT path, so web apps that expect to be
	// served from "/" work. Otherwise serve the dashboard/API.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		label := host
		if i := strings.IndexByte(host, '.'); i >= 0 {
			label = host[:i]
		}
		if label != "" && len(links.Templates) > 0 {
			for _, c := range reg.list() {
				if links.mainHost(c.Name) == label {
					port := mainPort(c)
					if port == 0 {
						http.Error(w, "client has no forwarded service", http.StatusBadGateway)
						return
					}
					target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
					(&httputil.ReverseProxy{
						// Rewrite (not Director): no X-Forwarded-* added and Host set
						// to localhost, so the backend accepts it as a direct request.
						Rewrite: func(pr *httputil.ProxyRequest) {
							pr.SetURL(target)
							pr.Out.Host = target.Host
						},
					}).ServeHTTP(w, r)
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
	return http.ListenAndServe(addr, root)
}

func onlyAlnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mainPort returns a client's lowest forwarded port, taken as its main web
// service (by convention the extra forwards — stats, speedtest — use higher
// port numbers).
func mainPort(c *Client) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var m uint32
	for _, p := range c.ports {
		if m == 0 || p < m {
			m = p
		}
	}
	return m
}
