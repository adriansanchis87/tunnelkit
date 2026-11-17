package monitor

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// A failed scrape (e.g. timeout while a speedtest saturates the link) must not
// wipe what the last good one reported.
func TestScrapeKeepsLastGoodMetrics(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"reconnects":7,"speedtest_port":8243,"last_error":"boom"}`))
	}))
	defer srv.Close()
	_, ps, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(ps)

	c := &Client{Name: "tk-x-web", ports: []uint32{uint32(port)}}
	c.scrape()
	if c.speedPort != 8243 || c.reconnects != 7 || c.lastError != "boom" {
		t.Fatalf("after good scrape: speedPort=%d reconnects=%d err=%q", c.speedPort, c.reconnects, c.lastError)
	}
	fail.Store(true)
	c.scrape()
	if c.speedPort != 8243 || c.reconnects != 7 || c.lastError != "boom" {
		t.Errorf("failed scrape wiped the metrics: speedPort=%d reconnects=%d err=%q", c.speedPort, c.reconnects, c.lastError)
	}
}
