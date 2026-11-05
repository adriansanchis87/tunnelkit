package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// probeResult is what the "Test" button in the panel shows for one port.
type probeResult struct {
	Kind      string  `json:"kind"`  // "http" or "tcp"
	Level     string  `json:"level"` // "ok", "warn" or "fail"
	Message   string  `json:"message"`
	Status    int     `json:"status,omitempty"` // HTTP status, when there was one
	LatencyMS float64 `json:"latency_ms"`
}

const (
	httpProbeTimeout = 6 * time.Second
	tcpProbeTimeout  = 3 * time.Second
	tcpProbeWait     = 1500 * time.Millisecond
)

// probe checks a forwarded port THROUGH the tunnel: addr is the listener the
// server itself holds for that port (127.0.0.1:<port>), so a connection there
// travels to the client and on to its local service.
//
// With an http(s) expected URL it makes a real request (Host header and TLS
// server name taken from the URL, certificate not verified: it is a reachability
// probe against loopback and sends nothing sensitive). Otherwise it only opens a
// TCP connection and watches what happens next.
func probe(addr, rawURL string) probeResult {
	if u, err := url.Parse(rawURL); err == nil && rawURL != "" {
		if s := strings.ToLower(u.Scheme); s == "http" || s == "https" {
			return probeHTTP(addr, u)
		}
	}
	return probeTCP(addr)
}

func probeHTTP(addr string, u *url.URL) probeResult {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: tcpProbeTimeout}).DialContext(ctx, network, addr)
		},
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()}, //nolint:gosec // loopback reachability probe
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: httpProbeTimeout,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{
		Transport: tr,
		Timeout:   httpProbeTimeout,
		// A redirect is a perfectly good sign of life; do not chase it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return probeResult{Kind: "http", Level: "fail", Message: "bad URL: " + err.Error()}
	}
	req.Header.Set("User-Agent", "tunnelkit-probe")
	start := time.Now()
	resp, err := cl.Do(req)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return probeResult{Kind: "http", Level: "fail", LatencyMS: ms, Message: explainErr(err, u.Scheme)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	r := probeResult{Kind: "http", Status: resp.StatusCode, LatencyMS: ms}
	switch {
	case resp.StatusCode < 400:
		r.Level, r.Message = "ok", fmt.Sprintf("the service answered HTTP %d", resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		r.Level, r.Message = "ok", fmt.Sprintf("the service answered HTTP %d (it is up, login required)", resp.StatusCode)
	case resp.StatusCode >= 500:
		r.Level, r.Message = "warn", fmt.Sprintf("the service answered HTTP %d: reached, but it reports an error", resp.StatusCode)
	default:
		r.Level, r.Message = "warn", fmt.Sprintf("the service answered HTTP %d: reached, but not the expected page", resp.StatusCode)
	}
	return r
}

func probeTCP(addr string) probeResult {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, tcpProbeTimeout)
	if err != nil {
		return probeResult{Kind: "tcp", Level: "fail", Message: "the port is not listening on the server: " + err.Error()}
	}
	defer c.Close()
	// Accepting is not enough: the server accepts first and only then asks the
	// client to open the local connection. If that fails the server closes at
	// once, so wait briefly to see which of the three things happens.
	_ = c.SetReadDeadline(time.Now().Add(tcpProbeWait))
	var b [1]byte
	_, err = c.Read(b[:])
	ms := float64(time.Since(start).Microseconds()) / 1000
	var ne net.Error
	switch {
	case err == nil:
		return probeResult{Kind: "tcp", Level: "ok", LatencyMS: ms, Message: "the service sent data: it is up"}
	case errors.As(err, &ne) && ne.Timeout():
		return probeResult{Kind: "tcp", Level: "ok", LatencyMS: ms,
			Message: "connection accepted and held open: the service is up (it waits for the client to speak first)"}
	default:
		return probeResult{Kind: "tcp", Level: "fail", LatencyMS: ms,
			Message: "the tunnel closed the connection at once: the client could not reach its local service"}
	}
}

// explainErr turns the usual failures into something an operator can act on.
func explainErr(err error, scheme string) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "first record does not look like a TLS handshake"),
		strings.Contains(s, "server gave HTTP response to HTTPS client"):
		return "the URL says https but the service speaks plain HTTP"
	case strings.Contains(s, "EOF"), strings.Contains(s, "connection reset"),
		strings.Contains(s, "server closed idle connection"), strings.Contains(s, "broken pipe"):
		return "the tunnel closed the connection at once: the client could not reach its local service"
	case strings.Contains(s, "tls:") && strings.EqualFold(scheme, "http"):
		return "TLS error on an http URL: " + s
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return fmt.Sprintf("no answer within %s", httpProbeTimeout)
	case strings.Contains(s, "refused"):
		return "the port is not listening on the server"
	}
	return s
}

// probeGate limits how often the same client/port can be probed, since the
// endpoint is reachable by anyone who can open the panel.
type probeGate struct {
	mu   sync.Mutex
	last map[string]time.Time
	min  time.Duration
}

func newProbeGate(min time.Duration) *probeGate {
	return &probeGate{last: map[string]time.Time{}, min: min}
}

// allow reports whether key may be probed now and records the attempt.
func (g *probeGate) allow(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if t, ok := g.last[key]; ok && now.Sub(t) < g.min {
		return false
	}
	g.last[key] = now
	if len(g.last) > 1024 { // never grow without bound
		for k, t := range g.last {
			if now.Sub(t) > time.Minute {
				delete(g.last, k)
			}
		}
	}
	return true
}
