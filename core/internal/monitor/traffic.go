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

// upEvent is an availability transition: at unix time T the client's connected
// state BECAME U. The state during [events[i].T, events[i+1].T) is events[i].U.
// A compact log (only transitions) so uptime over any window is reconstructable
// and it survives restarts (persisted with the traffic store).
type upEvent struct {
	T int64 `json:"t"`
	U bool  `json:"u"`
}

// ct: a client's traffic, by port and by day. Persistable.
type ct struct {
	Ports    map[uint32]*pb    `json:"ports"`
	Days     map[string]uint64 `json:"days"`      // "2006-01-02" -> bytes (tx+rx)
	LastSeen int64             `json:"last_seen"` // unix: last time the client was connected
	Up       []upEvent         `json:"up,omitempty"`
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
	// Seed LastSeen for entries loaded from an older state file (field absent).
	// We don't know their real last-seen time, so we baseline it to now; going
	// forward Touch() keeps it accurate.
	now := time.Now().Unix()
	for _, c := range t.m {
		if c.LastSeen == 0 {
			c.LastSeen = now
		}
	}
	go t.saver()
	return t
}

// Touch records that a client is currently connected (updates LastSeen to now).
// Creates the client entry if it is not known yet.
func (t *TrafficStore) Touch(name string) {
	if name == "" {
		return
	}
	t.mu.Lock()
	c := t.m[name]
	if c == nil {
		c = &ct{Ports: map[uint32]*pb{}, Days: map[string]uint64{}}
		t.m[name] = c
	}
	c.LastSeen = time.Now().Unix()
	t.dirty = true
	t.mu.Unlock()
}

// RecordState appends an availability transition for a client, but only when the
// state actually changes (the log stays compact). Creates the entry if unknown.
func (t *TrafficStore) RecordState(name string, up bool) {
	if name == "" {
		return
	}
	t.mu.Lock()
	c := t.m[name]
	if c == nil {
		c = &ct{Ports: map[uint32]*pb{}, Days: map[string]uint64{}}
		t.m[name] = c
	}
	if n := len(c.Up); n == 0 || c.Up[n-1].U != up {
		c.Up = append(c.Up, upEvent{T: time.Now().Unix(), U: up})
		t.dirty = true
	}
	t.mu.Unlock()
}

// uptimeSeries returns a copy of a client's availability transition log.
func (t *TrafficStore) uptimeSeries(name string) []upEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.m[name]
	if c == nil {
		return nil
	}
	return append([]upEvent(nil), c.Up...)
}

// uptimePct returns the % of the last windowSec seconds the client was connected,
// over the portion for which we have data. Returns -1 when there is no data.
func (t *TrafficStore) uptimePct(name string, windowSec int64) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.m[name]
	if c == nil || len(c.Up) == 0 {
		return -1
	}
	ev := c.Up
	now := time.Now().Unix()
	from := now - windowSec
	if ds := ev[0].T; ds > from { // no data before the first event
		from = ds
	}
	st := ev[0].U
	for _, e := range ev {
		if e.T <= from {
			st = e.U
		} else {
			break
		}
	}
	cursor, cur := from, st
	var upDur int64
	for _, e := range ev {
		if e.T <= from {
			continue
		}
		if e.T >= now {
			break
		}
		if cur {
			upDur += e.T - cursor
		}
		cursor, cur = e.T, e.U
	}
	if cur {
		upDur += now - cursor
	}
	cov := now - from
	if cov <= 0 {
		return -1
	}
	return float64(upDur) / float64(cov) * 100
}

// Seen returns a snapshot of every known client's last-seen unix time.
func (t *TrafficStore) Seen() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int64, len(t.m))
	for name, c := range t.m {
		out[name] = c.LastSeen
	}
	return out
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
		cut := time.Now().Add(-31 * 24 * time.Hour).Unix()
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
			// prune availability events older than 31 days, but keep the last one
			// before the cutoff (it carries the state entering the window); cap total.
			if len(c.Up) > 1 {
				keep := 0
				for i, e := range c.Up {
					if e.T < cut {
						keep = i
					} else {
						break
					}
				}
				if keep > 0 {
					c.Up = append([]upEvent(nil), c.Up[keep:]...)
				}
			}
			if len(c.Up) > 8000 {
				c.Up = c.Up[len(c.Up)-8000:]
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
