package monitor

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Prometheus text exposition of the server's own view of every client. The
// traffic counters come from the persisted TrafficStore, so they are monotonic
// across client reconnects and server restarts (rate()/increase() just work).

func promEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

// trafficSnapshot copies the cumulative per-client, per-port byte counters.
func (t *TrafficStore) trafficSnapshot() map[string]map[uint32]pb {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]map[uint32]pb, len(t.m))
	for name, c := range t.m {
		ports := make(map[uint32]pb, len(c.Ports))
		for p, b := range c.Ports {
			ports[p] = *b
		}
		out[name] = ports
	}
	return out
}

// writeProm renders the metrics for every known client (connected or not).
func writeProm(w io.Writer, reg *Registry, store *TrafficStore, meta *MetaStore) {
	rows := map[string]row{}
	for _, c := range reg.list() {
		rows[c.Name] = c.row()
	}
	names := map[string]bool{}
	for n := range rows {
		names[n] = true
	}
	var traffic map[string]map[uint32]pb
	if store != nil {
		for n := range store.Seen() {
			names[n] = true
		}
		traffic = store.trafficSnapshot()
		for n := range traffic {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	lbl := func(n string) string {
		return fmt.Sprintf(`client="%s",title="%s"`, promEsc(n), promEsc(meta.Get(n).Title))
	}
	head := func(name, typ, help string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}

	head("tunnelkit_client_up", "gauge", "1 if the client has a tunnel up right now.")
	for _, n := range sorted {
		fmt.Fprintf(w, "tunnelkit_client_up{%s} %g\n", lbl(n), b2f(rows[n].Connected))
	}
	head("tunnelkit_client_uptime_seconds", "gauge", "Age of the current tunnel session.")
	head("tunnelkit_client_reconnects", "gauge", "Reconnections reported by the client since it started.")
	head("tunnelkit_client_latency_ms", "gauge", "Round trip of the last scrape of the client through the tunnel.")
	head("tunnelkit_client_active_connections", "gauge", "Connections currently flowing through the client's forwarded ports.")
	head("tunnelkit_client_speedtest_mbps", "gauge", "Last speedtest result, by direction.")
	for _, n := range sorted {
		r, ok := rows[n]
		if !ok {
			continue
		}
		l := lbl(n)
		fmt.Fprintf(w, "tunnelkit_client_uptime_seconds{%s} %g\n", l, r.Uptime)
		fmt.Fprintf(w, "tunnelkit_client_reconnects{%s} %d\n", l, r.Reconnects)
		fmt.Fprintf(w, "tunnelkit_client_latency_ms{%s} %g\n", l, r.Latency)
		fmt.Fprintf(w, "tunnelkit_client_active_connections{%s} %d\n", l, r.Active)
		if r.LastDown > 0 || r.LastUp > 0 {
			fmt.Fprintf(w, "tunnelkit_client_speedtest_mbps{%s,direction=\"down\"} %g\n", l, r.LastDown)
			fmt.Fprintf(w, "tunnelkit_client_speedtest_mbps{%s,direction=\"up\"} %g\n", l, r.LastUp)
		}
	}
	if store != nil {
		head("tunnelkit_client_availability_24h_ratio", "gauge", "Fraction of the last 24 h the client was connected.")
		for _, n := range sorted {
			if p := store.uptimePct(n, 86400); p >= 0 {
				fmt.Fprintf(w, "tunnelkit_client_availability_24h_ratio{%s} %g\n", lbl(n), p/100)
			}
		}
		head("tunnelkit_traffic_bytes_total", "counter",
			"Bytes carried through a forwarded port since the store began (tx = client sends, rx = client receives).")
		for _, n := range sorted {
			ports := make([]uint32, 0, len(traffic[n]))
			for p := range traffic[n] {
				ports = append(ports, p)
			}
			sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
			for _, p := range ports {
				b := traffic[n][p]
				l := fmt.Sprintf(`%s,port="%d",service="%s"`, lbl(n), p, promEsc(meta.Port(n, p).Name))
				fmt.Fprintf(w, "tunnelkit_traffic_bytes_total{%s,direction=\"tx\"} %d\n", l, b.Tx)
				fmt.Fprintf(w, "tunnelkit_traffic_bytes_total{%s,direction=\"rx\"} %d\n", l, b.Rx)
			}
		}
	}
}
