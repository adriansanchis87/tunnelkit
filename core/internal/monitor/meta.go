package monitor

import (
	"bytes"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"regexp"
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
//	  "site1-web": {
//	    "title": "Site 1 - web server",
//	    "host": "site1",        // optional: subdomain of the main URL (any name)
//	    "ports": {
//	      "8080": {"name": "Web UI", "url": "https://web.site1.example.com", "default": true},
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
	Title string `json:"title,omitempty"`
	// Host is the subdomain label of the client's main URL (the panel appends
	// its own parent domain). It is the only source of the main URL: a client's name
	// does not have to follow any pattern.
	Host  string              `json:"host,omitempty"`
	Ports map[string]PortMeta `json:"ports,omitempty"`

	// order is the order the ports appear in the file (a Go map forgets it).
	// It decides the default port: the first one with an http(s) URL.
	order []string
}

// UnmarshalJSON reads the client normally and also records the order of its
// "ports" keys as written in the file.
func (c *ClientMeta) UnmarshalJSON(b []byte) error {
	type plain ClientMeta
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = ClientMeta(p)
	c.order = portKeyOrder(b)
	return nil
}

// portKeyOrder returns the keys of the "ports" object of a client, in file order.
func portKeyOrder(b []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		kt, err := dec.Token()
		key, _ := kt.(string)
		if err != nil {
			return nil
		}
		if key != "ports" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return nil
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return nil
		}
		var keys []string
		for dec.More() {
			kt, err := dec.Token()
			k, _ := kt.(string)
			if err != nil {
				return keys
			}
			keys = append(keys, k)
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return keys
			}
		}
		return keys
	}
	return nil
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

// DefaultPort is the client's default port: the FIRST port in the file's "ports"
// list that has an http(s) URL (0 when none), so the operator picks it by
// ordering the list. Its URL is what the client's name opens in the panel and the
// port the panel serves at the client's host label.
func (m *MetaStore) DefaultPort(name string) int {
	c := m.Get(name)
	for _, k := range c.order {
		if n, err := strconv.Atoi(k); err == nil && isWebURL(c.Ports[k].URL) {
			return n
		}
	}
	return 0
}

// keysInOrder lists a client's port keys in file order (each once), followed by
// any key the order did not record (sorted), so nothing is lost.
func keysInOrder(c ClientMeta) []string {
	seen := make(map[string]bool, len(c.Ports))
	var out []string
	for _, k := range c.order {
		if _, ok := c.Ports[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	var rest []string
	for k := range c.Ports {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func isWebURL(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
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
	hosts := map[string]string{} // label -> client that owns it
	for _, name := range names {
		c := in[name]
		oc := ClientMeta{Title: clip(c.Title, maxTitleLen)}
		seenPort := map[int]bool{}
		if h := strings.ToLower(strings.TrimSpace(c.Host)); h != "" {
			switch {
			case !hostLabel.MatchString(h):
				warn(name + ": host \"" + c.Host + "\" must be a single DNS label (letters, digits, '-'); ignored")
			case hosts[h] != "":
				warn(name + ": host \"" + h + "\" is already used by " + hosts[h] + "; ignored")
			default:
				hosts[h], oc.Host = name, h
			}
		}
		for _, key := range keysInOrder(c) {
			p := c.Ports[key]
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
			if _, dup := seenPort[port]; !dup {
				seenPort[port] = true
				oc.order = append(oc.order, strconv.Itoa(port))
			}
		}
		out[name] = oc
	}
	return out
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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
