package monitor

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// pb: accumulated bytes of a forwarded port (client's perspective).
type pb struct {
	Tx uint64 `json:"tx"`
	Rx uint64 `json:"rx"`
}

// ct: a client's traffic, by port and by day. Persistable.
type ct struct {
	Ports map[uint32]*pb    `json:"ports"`
	Days  map[string]uint64 `json:"days"` // "2006-01-02" -> bytes (tx+rx)
}

// TrafficStore accumulates traffic by client NAME (survives reconnections and
// restarts: it is persisted to disk).
type TrafficStore struct {
	mu    sync.Mutex
	path  string
	m     map[string]*ct
	dirty bool
}

func today() string { return time.Now().Format("2006-01-02") }

// NewTrafficStore loads the state from disk (if present) and starts the saver.
func NewTrafficStore(path string) *TrafficStore {
	t := &TrafficStore{path: path, m: map[string]*ct{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &t.m)
	}
	go t.saver()
	return t
}

func (t *TrafficStore) Add(name string, port uint32, tx, rx uint64) {
	if name == "" {
		return
	}
	t.mu.Lock()
	c := t.m[name]
	if c == nil {
		c = &ct{Ports: map[uint32]*pb{}, Days: map[string]uint64{}}
		t.m[name] = c
	}
	p := c.Ports[port]
	if p == nil {
		p = &pb{}
		c.Ports[port] = p
	}
	p.Tx += tx
	p.Rx += rx
	c.Days[today()] += tx + rx
	t.dirty = true
	t.mu.Unlock()
}

// portRow/dayRow: sorted rows for the dialog.
type portRow struct {
	Port uint32 `json:"port"`
	Tx   uint64 `json:"tx"`
	Rx   uint64 `json:"rx"`
}
type dayRow struct {
	Date  string `json:"date"`
	Bytes uint64 `json:"bytes"`
}

func (t *TrafficStore) breakdown(name string) (ports []portRow, days []dayRow) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.m[name]
	if c == nil {
		return
	}
	for port, p := range c.Ports {
		ports = append(ports, portRow{port, p.Tx, p.Rx})
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	for d, b := range c.Days {
		days = append(days, dayRow{d, b})
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date > days[j].Date })
	if len(days) > 30 {
		days = days[:30]
	}
	return
}

// todayTotal returns a client's total for today.
func (t *TrafficStore) todayTotal(name string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.m[name]; c != nil {
		return c.Days[today()]
	}
	return 0
}

func (t *TrafficStore) saver() {
	for range time.Tick(30 * time.Second) {
		t.mu.Lock()
		if !t.dirty || t.path == "" {
			t.mu.Unlock()
			continue
		}
		// prune old days (>60) to avoid growing without bound
		for _, c := range t.m {
			if len(c.Days) > 60 {
				var ds []string
				for d := range c.Days {
					ds = append(ds, d)
				}
				sort.Strings(ds)
				for _, d := range ds[:len(ds)-60] {
					delete(c.Days, d)
				}
			}
		}
		data, _ := json.Marshal(t.m)
		t.dirty = false
		t.mu.Unlock()
		tmp := t.path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			os.Rename(tmp, t.path)
		}
	}
}
