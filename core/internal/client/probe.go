package client

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// probeTimeoutVar bounds each phase (TCP connect, SSH banner) of the pre-flight
// probe (a var so tests can shorten it).
var probeTimeoutVar = 10 * time.Second

// probe checks, before ssh is started, how far a connection to the server
// gets: TCP connect, then the SSH identification line the server sends first.
// It exists because ssh without a connect timeout can sit for a very long time
// on a connection that opens but never delivers the banner (seen through a
// satellite link after a modem reboot), printing nothing. The returned text
// states what was observed.
func probe(host string, port int) (string, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	t0 := time.Now()
	c, err := net.DialTimeout("tcp", addr, probeTimeoutVar)
	if err != nil {
		return "", fmt.Errorf("TCP connect to %s failed after %s: %v", addr, time.Since(t0).Round(time.Millisecond), err)
	}
	defer c.Close()
	conn := time.Since(t0)
	_ = c.SetReadDeadline(time.Now().Add(probeTimeoutVar))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("TCP connected to %s in %s but no SSH banner within %s (%v): "+
			"the connection opens and then stalls or is filtered", addr, conn.Round(time.Millisecond), probeTimeoutVar, err)
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "SSH-") {
		return "", fmt.Errorf("%s answered with something that is not SSH: %q", addr, line)
	}
	return fmt.Sprintf("TCP %s in %s, banner %q in %s", addr, conn.Round(time.Millisecond), line, time.Since(t0).Round(time.Millisecond)), nil
}
