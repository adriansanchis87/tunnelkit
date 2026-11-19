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
	"strconv"
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
	mu     sync.Mutex
	m      map[string]*Client
	lastIP map[string]string // client name -> IP of its last connection
	att    *Attempts
	hb     func(name string) (time.Time, string, bool) // UDP heartbeat lookup (nil = none)
}

func NewRegistry() *Registry {
	return &Registry{m: map[string]*Client{}, lastIP: map[string]string{}, att: NewAttempts()}
}

func (r *Registry) Add(id string, c *Client) {
	r.mu.Lock()
	r.m[id] = c
	if c.Name != "" && c.IP != "" {
		r.lastIP[c.Name] = c.IP
	}
	r.mu.Unlock()
}

// SetHeartbeat plugs the UDP heartbeat source: it returns when and from which
// IP the client's last heartbeat arrived.
func (r *Registry) SetHeartbeat(f func(name string) (time.Time, string, bool)) {
	r.mu.Lock()
	r.hb = f
	r.mu.Unlock()
}

// Connected reports whether the named client has a TCP tunnel up right now.
func (r *Registry) Connected(name string) bool { return r.byName(name) != nil }

// heartbeat returns seconds since the client's last UDP heartbeat (nil when the
// source is off or none was ever received) and the IP it came from.
func (r *Registry) heartbeat(name string) (*float64, string) {
	r.mu.Lock()
	f := r.hb
	r.mu.Unlock()
	if f == nil {
		return nil, ""
	}
	at, ip, ok := f(name)
	if !ok {
		return nil, ""
	}
	ago := time.Since(at).Seconds()
	return &ago, ip
}

// Attempts is the record of connections that reached the server but failed.
func (r *Registry) Attempts() *Attempts { return r.att }

// LastIP is the IP a known client last connected from ("" if unknown).
func (r *Registry) LastIP(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastIP[name]
}

// ClientAt returns the name of the known client whose last connection came
// from ip ("" if none); several clients behind one NAT share it, so it returns
// the names joined by "/".
func (r *Registry) ClientAt(ip string) string {
	r.mu.Lock()
	var names []string
	for n, i := range r.lastIP {
		if i == ip {
			names = append(names, n)
		}
	}
	r.mu.Unlock()
	sort.Strings(names)
	return strings.Join(names, "/")
}
func (r *Registry) Del(id string)         { r.mu.Lock(); delete(r.m, id); r.mu.Unlock() }
func (r *Registry) Get(id string) *Client { r.mu.Lock(); defer r.mu.Unlock(); return r.m[id] }

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
	var got bool // a /metrics answer was read and parsed
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
			got = true
			lat = float64(time.Since(t).Microseconds()) / 1000
			break
		}
	}

	now := time.Now()
	tx, rx := c.tx.Load(), c.rx.Load()
	c.mu.Lock()
	// Only trust an answer we actually got. While a speedtest saturates the
	// link the scrape times out; overwriting with zero values would make the
	// panel think the client has no speedtest (and reset its reconnect count).
	if got {
		c.reconnects, c.lastError, c.speedPort = m.Reconnects, m.LastError, m.SpeedtestPort
	}
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

// portInfo is one forwarded port with its operator-provided description.
type portInfo struct {
	Port uint32 `json:"port"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"` // expected URL (http, https, tcp or ssh)
	// Default marks the client's default port: the first one in clients.json's
	// list with an http(s) URL. Its URL is what the client's name opens.
	Default bool `json:"default,omitempty"`
}

type row struct {
	Name         string     `json:"name"`
	Title        string     `json:"title,omitempty"`     // display title from clients.json
	PortInfo     []portInfo `json:"port_info,omitempty"` // ports with their names / expected URLs
	Connected    bool       `json:"connected"`
	IP           string     `json:"ip"`
	Ports        []uint32   `json:"ports"`
	Uptime       float64    `json:"uptime_seconds"`
	Reconnects   int        `json:"reconnects"`
	Latency      float64    `json:"latency_ms"`
	Active       int64      `json:"active"`
	BytesTx      uint64     `json:"bytes_tx"`
	BytesRx      uint64     `json:"bytes_rx"`
	LastError    string     `json:"last_error"`
	Hist         []sample   `json:"hist"`
	Speedtest    bool       `json:"speedtest"` // responder present
	LastDown     float64    `json:"last_down"`
	LastUp       float64    `json:"last_up"`
	SpeedHist    []float64  `json:"speed_hist"`
	TrafficToday uint64     `json:"traffic_today"` // today's bytes (tx+rx)
	// OfflineSeconds is how long a KNOWN client has been disconnected. It is
	// only set on rows where Connected is false (built from the traffic store's
	// last-seen record); 0 on connected clients.
	OfflineSeconds float64 `json:"offline_seconds"`
	// Uptime24h is the % of the last 24h the client was connected (from the
	// availability log); -1 when there is no data.
	Uptime24h float64 `json:"uptime_24h"`
	// Links are the web entry points the panel offers for this client, built
	// from the client's "host" in clients.json (empty when it has none).
	Links []link `json:"links,omitempty"`
	// The fields below are only set on OFFLINE rows. They say what the server
	// saw from the client's last known IP since it went down: nothing at all,
	// or connections that arrived and failed (and how).
	LastIP           string   `json:"last_ip,omitempty"`
	LastAttemptAgo   *float64 `json:"last_attempt_seconds_ago,omitempty"`
	LastAttemptError string   `json:"last_attempt_error,omitempty"`
	FailedAttempts   int      `json:"failed_attempts,omitempty"`
	Diagnosis        string   `json:"diagnosis,omitempty"`
	// UDP heartbeat (set on connected and offline rows when the client has ever
	// sent one): seconds since the last beat and the IP it came from. Compare
	// with Connected: TCP down + beats fresh = only TCP is broken on the path.
	UDPSecondsAgo *float64 `json:"udp_seconds_ago,omitempty"`
	UDPIP         string   `json:"udp_ip,omitempty"`
}

// link is the web entry point of a client: Host is the subdomain label only
// (the panel prepends the scheme and appends its own parent domain). It comes
// from the client's "host" in clients.json.
type link struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Host  string `json:"host"`
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

// describe fills the presentation fields of a row from the metadata store.
// Every port the client forwards is listed (so unnamed ones still show), plus
// any described port that is not currently forwarded.
func describe(r *row, meta *MetaStore) {
	m := meta.Get(r.Name)
	r.Title = m.Title
	def := uint32(meta.DefaultPort(r.Name))
	seen := map[uint32]bool{}
	for _, p := range r.Ports {
		pm := meta.Port(r.Name, p)
		r.PortInfo = append(r.PortInfo, portInfo{Port: p, Name: pm.Name, URL: pm.URL, Default: p == def})
		seen[p] = true
	}
	for k, pm := range m.Ports {
		var n uint32
		if _, err := fmt.Sscanf(k, "%d", &n); err == nil && !seen[n] {
			r.PortInfo = append(r.PortInfo, portInfo{Port: n, Name: pm.Name, URL: pm.URL, Default: n == def})
		}
	}
	sort.Slice(r.PortInfo, func(i, j int) bool { return r.PortInfo[i].Port < r.PortInfo[j].Port })
}

// Serve starts the web panel (blocking) + the background sampler.
func Serve(addr string, reg *Registry, store *TrafficStore, meta *MetaStore) error {
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
		for _, c := range reg.list() {
			r := c.row()
			r.Uptime24h = -1
			if store != nil {
				r.TrafficToday = store.todayTotal(c.Name)
				r.Uptime24h = store.uptimePct(c.Name, 86400)
			}
			r.Links = linksFor(meta, c.Name)
			describe(&r, meta)
			r.UDPSecondsAgo, r.UDPIP = reg.heartbeat(c.Name)
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
				ip := reg.LastIP(name)
				diag, ago, lerr, fails := reg.Attempts().Diagnose(ip, lastSeen)
				orow := row{
					Name:             name,
					Connected:        false,
					TrafficToday:     store.todayTotal(name),
					OfflineSeconds:   off,
					Uptime24h:        store.uptimePct(name, 86400),
					Links:            linksFor(meta, name),
					LastIP:           ip,
					LastAttemptError: lerr,
					FailedAttempts:   fails,
					Diagnosis:        diag,
				}
				if ago >= 0 {
					orow.LastAttemptAgo = &ago
				}
				orow.UDPSecondsAgo, orow.UDPIP = reg.heartbeat(name)
				orow.Diagnosis += udpDiagnosis(orow.UDPSecondsAgo, orow.UDPIP)
				describe(&orow, meta)
				rows = append(rows, orow)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"generated_at": time.Now().Unix(), "clients": rows,
			// Newest first: connections that reached the server and failed.
			"failed_handshakes": reg.Attempts().Recent(20),
		})
	})
	// Prometheus text format, scraped over the internal network.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeProm(w, reg, store, meta)
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
	// /api/probe?client=NAME&port=N checks one forwarded port through the tunnel
	// (see probe.go). Only ports the client currently forwards can be probed, so
	// it cannot be used to reach anything else on the server.
	gate := newProbeGate(time.Second)
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, req *http.Request) {
		name := req.URL.Query().Get("client")
		c := reg.byName(name)
		if c == nil {
			http.Error(w, "client not connected", http.StatusNotFound)
			return
		}
		port, err := strconv.Atoi(req.URL.Query().Get("port"))
		if err != nil {
			http.Error(w, "bad port", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		forwarded := false
		for _, p := range c.ports {
			if int(p) == port {
				forwarded = true
			}
		}
		c.mu.Unlock()
		if !forwarded {
			http.Error(w, "that port is not forwarded by this client", http.StatusBadRequest)
			return
		}
		if !gate.allow(name + ":" + strconv.Itoa(port)) {
			http.Error(w, "probed too recently, wait a second", http.StatusTooManyRequests)
			return
		}
		res := probe(fmt.Sprintf("127.0.0.1:%d", port), meta.Port(name, uint32(port)).URL)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
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
	// "host" in clients.json, reverse-proxy to that client's
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
		if label != "" {
			for _, c := range reg.list() {
				if h := meta.Get(c.Name).Host; h != "" && h == label {
					port := mainPort(c, meta.DefaultPort(c.Name))
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

// linksFor is the client's main link, from its "host" in clients.json.
func linksFor(meta *MetaStore, name string) []link {
	h := meta.Get(name).Host
	if h == "" {
		return nil
	}
	return []link{{Kind: "main", Label: name, Host: h}}
}

// mainPort returns a client's lowest forwarded port, taken as its main web
// service (by convention the extra forwards — stats, speedtest — use higher
// port numbers).
func mainPort(c *Client, preferred int) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return pickMain(c.ports, preferred)
}

// pickMain is the preferred port when it is forwarded, else the lowest one
// (0 when nothing is forwarded).
func pickMain(ports []uint32, preferred int) uint32 {
	var m uint32
	for _, p := range ports {
		if preferred > 0 && p == uint32(preferred) {
			return p
		}
		if m == 0 || p < m {
			m = p
		}
	}
	return m
}

// udpDiagnosis completes an offline client's diagnosis with what the UDP
// heartbeat says. It states facts: fresh beats mean UDP reaches the server.
func udpDiagnosis(ago *float64, ip string) string {
	if ago == nil {
		return "; no UDP heartbeat from this client (not configured, or none arrived since the server started)"
	}
	if *ago < 35 {
		return fmt.Sprintf("; UDP heartbeat is ALIVE (last %.0fs ago from %s): UDP reaches the server while the TCP tunnel is down", *ago, ip)
	}
	return fmt.Sprintf("; UDP heartbeat also silent for %s (last from %s): UDP is not reaching the server either",
		(time.Duration(*ago) * time.Second).Round(time.Second), ip)
}
