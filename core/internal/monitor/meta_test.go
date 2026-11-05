package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMeta(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "clients.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMetaLookup(t *testing.T) {
	p := writeMeta(t, t.TempDir(), `{"tk-a-web":{"title":"Site A","ports":{"8080":{"name":"Web UI","url":"https://a.example.com"},"8048":{"name":"Metrics"}}}}`)
	m := NewMetaStore(p)
	if got := m.Title("tk-a-web"); got != "Site A" {
		t.Errorf("title = %q", got)
	}
	if got := m.Title("tk-unknown"); got != "tk-unknown" {
		t.Errorf("fallback title = %q", got)
	}
	if pm := m.Port("tk-a-web", 8080); pm.Name != "Web UI" || pm.URL != "https://a.example.com" {
		t.Errorf("port meta = %+v", pm)
	}
	if pm := m.Port("tk-a-web", 9999); pm != (PortMeta{}) {
		t.Errorf("unknown port meta = %+v", pm)
	}
}

func TestMetaNoFileOrNil(t *testing.T) {
	if (*MetaStore)(nil).Title("x") != "x" {
		t.Error("nil store must fall back to the name")
	}
	m := NewMetaStore(filepath.Join(t.TempDir(), "missing.json"))
	if m.Title("x") != "x" || m.Port("x", 1) != (PortMeta{}) {
		t.Error("missing file must mean no metadata")
	}
	if NewMetaStore("").Title("x") != "x" {
		t.Error("empty path must mean no metadata")
	}
}

func TestMetaSanitizes(t *testing.T) {
	long := strings.Repeat("a", 500)
	p := writeMeta(t, t.TempDir(), `{"tk-a-web":{"title":"`+long+`","ports":{
	  "8080":{"name":"`+long+`","url":"javascript:alert(1)"},
	  "8081":{"url":"https://user:pw@example.com/"},
	  "8082":{"url":"file:///etc/passwd"},
	  "8083":{"url":"http:///nohost"},
	  "8084":{"url":"tcp://example.com:22"},
	  "notaport":{"name":"x"},
	  "70000":{"name":"x"},
	  " 8085 ":{"name":"spaces ok"}}}}`)
	m := NewMetaStore(p)
	c := m.Get("tk-a-web")
	if len([]rune(c.Title)) != maxTitleLen {
		t.Errorf("title not truncated: %d", len([]rune(c.Title)))
	}
	if len([]rune(c.Ports["8080"].Name)) != maxNameLen {
		t.Errorf("port name not truncated")
	}
	for _, port := range []string{"8080", "8081", "8082", "8083"} {
		if u := c.Ports[port].URL; u != "" {
			t.Errorf("port %s: dangerous/invalid url kept: %q", port, u)
		}
	}
	if c.Ports["8084"].URL != "tcp://example.com:22" {
		t.Errorf("tcp url dropped: %+v", c.Ports["8084"])
	}
	if _, ok := c.Ports["notaport"]; ok {
		t.Error("non-numeric port kept")
	}
	if _, ok := c.Ports["70000"]; ok {
		t.Error("out-of-range port kept")
	}
	if c.Ports["8085"].Name != "spaces ok" {
		t.Errorf("port key with spaces not normalised: %+v", c.Ports)
	}
}

func TestMetaReloadsAndSurvivesBadJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeMeta(t, dir, `{"tk-a-web":{"title":"One"}}`)
	m := NewMetaStore(p)
	if m.Title("tk-a-web") != "One" {
		t.Fatal("initial load")
	}
	// Change with a new mtime: picked up without restart.
	writeMeta(t, dir, `{"tk-a-web":{"title":"Two"}}`)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, future, future)
	if got := m.Title("tk-a-web"); got != "Two" {
		t.Errorf("not reloaded: %q", got)
	}
	// A broken edit must not wipe what was working.
	writeMeta(t, dir, `{ this is not json`)
	later := future.Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	if got := m.Title("tk-a-web"); got != "Two" {
		t.Errorf("bad JSON wiped the metadata: %q", got)
	}
	// Removing the file clears it.
	_ = os.Remove(p)
	if got := m.Title("tk-a-web"); got != "tk-a-web" {
		t.Errorf("removed file not cleared: %q", got)
	}
}

func TestDescribe(t *testing.T) {
	p := writeMeta(t, t.TempDir(), `{"tk-a-web":{"title":"Site A","ports":{"8080":{"name":"Web UI","url":"https://a.example.com"},"9000":{"name":"Not forwarded now"}}}}`)
	m := NewMetaStore(p)
	r := row{Name: "tk-a-web", Ports: []uint32{8048, 8080}}
	describe(&r, m)
	if r.Title != "Site A" {
		t.Errorf("title = %q", r.Title)
	}
	got := map[uint32]portInfo{}
	for _, pi := range r.PortInfo {
		got[pi.Port] = pi
	}
	if len(r.PortInfo) != 3 || got[8048].Name != "" || got[8080].Name != "Web UI" || got[9000].Name != "Not forwarded now" {
		t.Errorf("port_info = %+v", r.PortInfo)
	}
	// Sorted by port.
	if r.PortInfo[0].Port != 8048 || r.PortInfo[2].Port != 9000 {
		t.Errorf("not sorted: %+v", r.PortInfo)
	}
	// No metadata: ports still listed, no title.
	r2 := row{Name: "tk-x", Ports: []uint32{1}}
	describe(&r2, nil)
	if r2.Title != "" || len(r2.PortInfo) != 1 {
		t.Errorf("nil meta: %+v", r2)
	}
}
