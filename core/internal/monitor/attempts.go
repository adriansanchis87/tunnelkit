package monitor

import (
	"fmt"
	"sync"
	"time"
)

// Attempts remembers the connection attempts that REACHED the server but did
// not end in an authenticated session (handshake timeout, early close, auth
// failure), per remote IP. It exists to answer "is the client trying and
// failing, or is nothing arriving at all?" when a client goes offline.
type Attempts struct {
	mu   sync.Mutex
	ips  map[string]*ipStat
	ring []Attempt // newest last, at most ringLen
}

const (
	ringLen = 50
	// logEvery bounds how often failures from the same unknown IP are logged
	// (the SSH port gets scanned all day). Known clients are always logged.
	logEvery = 5 * time.Minute
)

// Attempt is one failed handshake.
type Attempt struct {
	At     int64  `json:"at"`
	IP     string `json:"ip"`
	Client string `json:"client,omitempty"` // guessed from the last IP of a known client
	Error  string `json:"error"`
	Millis int64  `json:"millis"` // how long the attempt lasted
}

type ipStat struct {
	lastAt     time.Time // last attempt (ok or failed)
	lastOK     time.Time
	lastErr    string
	failsSince int // failures since the last success
	lastLogged time.Time
	suppressed int
}

func NewAttempts() *Attempts { return &Attempts{ips: map[string]*ipStat{}} }

// Record notes the outcome of one connection from ip. err == nil means the
// session was authenticated. For failures it reports whether the caller should
// log it now and how many similar failures were suppressed since the last log.
//
// trivial marks a failure that carries no information (see the server): it only
// refreshes "last seen from this IP".
func (a *Attempts) Record(ip, client string, d time.Duration, err error, always, trivial bool) (logIt bool, suppressed int) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.ips[ip]
	if s == nil {
		s = &ipStat{}
		a.ips[ip] = s
	}
	s.lastAt = now
	if err == nil {
		s.lastOK, s.lastErr, s.failsSince = now, "", 0
		return false, 0
	}
	if trivial {
		return false, 0
	}
	s.lastErr = err.Error()
	s.failsSince++
	a.ring = append(a.ring, Attempt{At: now.Unix(), IP: ip, Client: client, Error: s.lastErr, Millis: d.Milliseconds()})
	if len(a.ring) > ringLen {
		a.ring = a.ring[len(a.ring)-ringLen:]
	}
	if always || s.lastLogged.IsZero() || now.Sub(s.lastLogged) >= logEvery {
		suppressed, s.suppressed, s.lastLogged = s.suppressed, 0, now
		return true, suppressed
	}
	s.suppressed++
	return false, 0
}

// Recent returns up to n failed attempts, newest first.
func (a *Attempts) Recent(n int) []Attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Attempt, 0, n)
	for i := len(a.ring) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, a.ring[i])
	}
	return out
}

// Diagnose explains, for a client that is offline since lastSeen (unix, 0 =
// unknown), what the server saw from its last known IP. It states facts only:
// what arrived and how it ended.
func (a *Attempts) Diagnose(ip string, lastSeen int64) (text string, lastAgo float64, lastErr string, fails int) {
	if ip == "" {
		return "no IP known for this client since the server started", -1, "", 0
	}
	a.mu.Lock()
	s := a.ips[ip]
	var st ipStat
	if s != nil {
		st = *s
	}
	a.mu.Unlock()
	down := time.Unix(lastSeen, 0)
	if s == nil || (lastSeen > 0 && !st.lastAt.After(down)) {
		return fmt.Sprintf("no connection from %s has reached the server since the client went down: "+
			"the client is off, or its packets are not arriving (path/NAT/middlebox)", ip), -1, "", 0
	}
	ago := time.Since(st.lastAt).Seconds()
	if st.failsSince == 0 {
		return fmt.Sprintf("%s connected again %s ago", ip, time.Since(st.lastOK).Round(time.Second)), ago, "", 0
	}
	return fmt.Sprintf("%d connection(s) from %s reached the server and failed; last %s ago: %s",
		st.failsSince, ip, time.Since(st.lastAt).Round(time.Second), st.lastErr), ago, st.lastErr, st.failsSince
}
