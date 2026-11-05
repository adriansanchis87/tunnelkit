package monitor

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Presentation metadata for clients and their ports, read from an optional JSON
// file next to authorized_keys (TK_SERVER_CLIENTS). It never affects
// authorization: access control stays in authorized_keys.
//
//	{
//	  "tk-site1-web": {
//	    "title": "Site 1 - web server",
//	    "ports": {
//	      "8080": {"name": "Web UI", "url": "https://web.site1.example.com"},
//	      "8048": {"name": "Metrics"}
//	    }
//	  }
//	}

// Limits keep a bad file from bloating the panel.
const (
	maxTitleLen = 100
	maxNameLen  = 60
	maxURLLen   = 300
)

// PortMeta describes one forwarded port.
type PortMeta struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"` // expected URL of the service (optional)
}

// ClientMeta describes one client.
type ClientMeta struct {
	Title string              `json:"title,omitempty"`
	Ports map[string]PortMeta `json:"ports,omitempty"`
}

// MetaStore holds the parsed file and reloads it when its mtime changes, like
// authorized_keys, so it can be edited without restarting the server.
type MetaStore struct {
	path string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	data  map[string]ClientMeta
	warn  map[string]bool // already-logged problems, to log each one once
}

// NewMetaStore returns a store for path. An empty path or a missing file means
// "no metadata": every lookup returns the zero value.
func NewMetaStore(path string) *MetaStore {
	return &MetaStore{path: path, warn: map[string]bool{}}
}

// Get returns the metadata of a client (zero value when there is none).
func (m *MetaStore) Get(name string) ClientMeta {
	if m == nil {
		return ClientMeta{}
	}
	m.refresh()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[name]
}

// Port returns the metadata of one port of a client.
func (m *MetaStore) Port(name string, port uint32) PortMeta {
	return m.Get(name).Ports[strconv.FormatUint(uint64(port), 10)]
}

// Title returns the display title of a client, falling back to its name.
func (m *MetaStore) Title(name string) string {
	if t := m.Get(name).Title; t != "" {
		return t
	}
	return name
}

func (m *MetaStore) refresh() {
	if m.path == "" {
		return
	}
	st, err := os.Stat(m.path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if m.data != nil { // file removed: forget everything
			m.data, m.mtime, m.size = nil, time.Time{}, 0
			log.Printf("monitor: %s removed, client metadata cleared", m.path)
		}
		return
	}
	if m.data != nil && st.ModTime().Equal(m.mtime) && st.Size() == m.size {
		return
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var in map[string]ClientMeta
	if err := json.Unmarshal(raw, &in); err != nil {
		// Keep the previous good data; remember the mtime so we do not retry
		// (and log) on every request until the file changes again.
		m.mtime, m.size = st.ModTime(), st.Size()
		log.Printf("monitor: %s: %v (keeping previous metadata)", m.path, err)
		return
	}
	m.data = sanitize(in, func(msg string) {
		if !m.warn[msg] {
			m.warn[msg] = true
			log.Printf("monitor: %s: %s", m.path, msg)
		}
	})
	m.mtime, m.size = st.ModTime(), st.Size()
	log.Printf("monitor: loaded metadata for %d client(s) from %s", len(m.data), m.path)
}

// sanitize trims and validates everything that will be shown in the panel.
func sanitize(in map[string]ClientMeta, warn func(string)) map[string]ClientMeta {
	out := make(map[string]ClientMeta, len(in))
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c := in[name]
		oc := ClientMeta{Title: clip(c.Title, maxTitleLen)}
		if c.Title != oc.Title {
			warn(name + ": title is longer than " + strconv.Itoa(maxTitleLen) + " characters, truncated")
		}
		for key, p := range c.Ports {
			port, err := strconv.Atoi(strings.TrimSpace(key))
			if err != nil || port < 1 || port > 65535 {
				warn(name + ": \"" + key + "\" is not a valid port, ignored")
				continue
			}
			op := PortMeta{Name: clip(p.Name, maxNameLen)}
			if u := strings.TrimSpace(p.URL); u != "" {
				if cleanURL(u) == "" {
					warn(name + ": port " + key + ": url must be http://, https://, tcp:// or ssh:// with a host; ignored")
				} else {
					op.URL = u
				}
			}
			if oc.Ports == nil {
				oc.Ports = map[string]PortMeta{}
			}
			oc.Ports[strconv.Itoa(port)] = op
		}
		out[name] = oc
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// cleanURL returns the URL when it is acceptable (known scheme, a host, not too
// long, no credentials), and "" otherwise. Only http and https are ever turned
// into clickable links by the panel; tcp and ssh are informational.
func cleanURL(raw string) string {
	if len(raw) > maxURLLen {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "tcp", "ssh":
		return raw
	}
	return ""
}
