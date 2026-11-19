package monitor

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePromTrafficAndUp(t *testing.T) {
	store := NewTrafficStore(filepath.Join(t.TempDir(), "t.json"))
	store.Add("a", 8140, 100, 0)
	store.Add("a", 8140, 0, 250)
	store.Add("off", 9000, 7, 0)
	store.Touch("off")
	reg := NewRegistry()
	reg.Add("id1", &Client{Name: "a", IP: "1.2.3.4"})

	var b bytes.Buffer
	writeProm(&b, reg, store, nil)
	out := b.String()
	for _, want := range []string{
		`tunnelkit_client_up{client="a",title=""} 1`,
		`tunnelkit_client_up{client="off",title=""} 0`,
		`tunnelkit_traffic_bytes_total{client="a",title="",port="8140",service="",direction="tx"} 100`,
		`tunnelkit_traffic_bytes_total{client="a",title="",port="8140",service="",direction="rx"} 250`,
		`tunnelkit_traffic_bytes_total{client="off",title="",port="9000",service="",direction="tx"} 7`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `uptime_seconds{client="off"`) {
		t.Error("offline client must not report session gauges")
	}
}

func TestPromEscape(t *testing.T) {
	if got := promEsc("a\"b\\c\nd"); got != `a\"b\\c\nd` {
		t.Fatal(got)
	}
}
