package client

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listen(t *testing.T, handle func(net.Conn)) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	return h, port
}

func TestProbeBanner(t *testing.T) {
	h, p := listen(t, func(c net.Conn) { c.Write([]byte("SSH-2.0-Go\r\n")); c.Close() })
	msg, err := probe(h, p)
	if err != nil || !strings.Contains(msg, "SSH-2.0-Go") {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
}

func TestProbeNotSSH(t *testing.T) {
	h, p := listen(t, func(c net.Conn) { c.Write([]byte("HTTP/1.1 400 Bad Request\r\n")); c.Close() })
	if _, err := probe(h, p); err == nil || !strings.Contains(err.Error(), "not SSH") {
		t.Fatalf("err=%v", err)
	}
}

func TestProbeConnectRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	p, _ := strconv.Atoi(ps)
	if _, err := probe("127.0.0.1", p); err == nil || !strings.Contains(err.Error(), "TCP connect") {
		t.Fatalf("err=%v", err)
	}
}

// The case that motivated the probe: TCP opens but no banner ever arrives.
func TestProbeStalled(t *testing.T) {
	h, p := listen(t, func(c net.Conn) { time.Sleep(3 * time.Second); c.Close() })
	old := probeTimeoutVar
	probeTimeoutVar = 500 * time.Millisecond
	defer func() { probeTimeoutVar = old }()
	_, err := probe(h, p)
	if err == nil || !strings.Contains(err.Error(), "no SSH banner") {
		t.Fatalf("err=%v", err)
	}
}
