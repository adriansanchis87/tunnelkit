package monitor

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func addrOf(s *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://")
}

// rawListener accepts connections and hands each to handle.
func rawListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return ln.Addr().String()
}

func TestProbeHTTPStatuses(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		switch r.URL.Path {
		case "/login":
			w.WriteHeader(401)
		case "/boom":
			w.WriteHeader(503)
		case "/missing":
			w.WriteHeader(404)
		case "/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		path   string
		level  string
		status int
	}{
		{"/", "ok", 200}, {"/login", "ok", 401}, {"/moved", "ok", 302},
		{"/missing", "warn", 404}, {"/boom", "warn", 503},
	} {
		r := probe(addrOf(srv), "http://svc.example.com"+tc.path)
		if r.Kind != "http" || r.Level != tc.level || r.Status != tc.status {
			t.Errorf("%s: %+v", tc.path, r)
		}
	}
	// The URL's host is sent as Host even though the connection goes to loopback.
	if gotHost != "svc.example.com" {
		t.Errorf("Host header = %q", gotHost)
	}
}

func TestProbeHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	// Self-signed certificate for another name: still reported as reachable.
	if r := probe(addrOf(srv), "https://svc.example.com/"); r.Level != "ok" || r.Status != 200 {
		t.Errorf("https: %+v", r)
	}
	// The public URL is https but TLS is terminated by a proxy: through the
	// tunnel the service speaks plain HTTP. That is a healthy service, and the
	// probe says so instead of failing.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer plain.Close()
	r := probe(addrOf(plain), "https://svc.example.com/")
	if r.Level != "ok" || r.Status != 200 || !strings.Contains(r.Message, "plain HTTP behind the tunnel") {
		t.Errorf("https URL, plain service: %+v", r)
	}
	// ...and a real failure behind it is still reported as a failure.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer broken.Close()
	if r := probe(addrOf(broken), "https://svc.example.com/"); r.Level != "warn" || r.Status != 503 {
		t.Errorf("https URL, plain 503 service: %+v", r)
	}
}

func TestProbeClosedImmediately(t *testing.T) {
	// What the tunnel does when the client cannot reach its local service.
	addr := rawListener(t, func(c net.Conn) { c.Close() })
	if r := probe(addr, ""); r.Level != "fail" || !strings.Contains(r.Message, "could not reach its local service") {
		t.Errorf("tcp: %+v", r)
	}
	if r := probe(addr, "http://svc.example.com/"); r.Level != "fail" || !strings.Contains(r.Message, "could not reach its local service") {
		t.Errorf("http: %+v", r)
	}
}

func TestProbeTCPBannerAndSilent(t *testing.T) {
	banner := rawListener(t, func(c net.Conn) {
		defer c.Close()
		c.Write([]byte("SSH-2.0-test\r\n"))
		time.Sleep(200 * time.Millisecond)
	})
	if r := probe(banner, "tcp://host:22"); r.Level != "ok" || r.Kind != "tcp" || !strings.Contains(r.Message, "sent data") {
		t.Errorf("banner: %+v", r)
	}
	silent := rawListener(t, func(c net.Conn) { defer c.Close(); time.Sleep(3 * time.Second) })
	if r := probe(silent, ""); r.Level != "ok" || !strings.Contains(r.Message, "held open") {
		t.Errorf("silent: %+v", r)
	}
}

func TestProbeNotListening(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if r := probe(addr, ""); r.Level != "fail" || !strings.Contains(r.Message, "not listening") {
		t.Errorf("tcp: %+v", r)
	}
	if r := probe(addr, "http://svc.example.com/"); r.Level != "fail" {
		t.Errorf("http: %+v", r)
	}
}

func TestProbeGate(t *testing.T) {
	g := newProbeGate(50 * time.Millisecond)
	if !g.allow("a") {
		t.Fatal("first probe must be allowed")
	}
	if g.allow("a") {
		t.Error("immediate second probe must be refused")
	}
	if !g.allow("b") {
		t.Error("other key must be independent")
	}
	time.Sleep(60 * time.Millisecond)
	if !g.allow("a") {
		t.Error("allowed again after the interval")
	}
}
