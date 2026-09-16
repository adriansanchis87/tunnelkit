package tunnelserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/adriansanchis87/tunnelkit/internal/monitor"
)

// --- helpers ---

func freePort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startTestServer spins up the tunnel server on 127.0.0.1:0 accepting the given
// client key with permitlisten over the given ports, all under name "tk-test".
// It returns the server address and a stop func.
func startTestServer(t *testing.T, clientKey ssh.PublicKey, name string, ports ...uint32) (string, func()) {
	t.Helper()
	host := newSigner(t)
	csv := ""
	for i, p := range ports {
		if i > 0 {
			csv += ","
		}
		csv += fmt.Sprint(p)
	}
	allowed := map[string]string{ssh.FingerprintSHA256(clientKey): csv}
	sconf := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			c, ok := allowed[ssh.FingerprintSHA256(key)]
			if !ok {
				return nil, fmt.Errorf("unauthorized")
			}
			return &ssh.Permissions{Extensions: map[string]string{"ports": c, "name": name}}, nil
		},
	}
	sconf.AddHostKey(host)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reg := monitor.NewRegistry()
	mgr := newSessionManager()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleConn(c, sconf, reg, mgr, nil)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// dialClient opens an SSH client connection (raw conn returned so a test can
// drop it abruptly). The caller closes the client.
func dialClient(t *testing.T, addr string, key ssh.Signer) (*ssh.Client, net.Conn) {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            "tunnel",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		t.Fatalf("ssh handshake: %v", err)
	}
	return ssh.NewClient(cc, chans, reqs), raw
}

// serveEcho accepts one connection on the remote-forwarded listener and echoes
// back the given tag followed by whatever it reads, so a test can tell which
// client answered.
func serveEcho(l net.Listener, tag string) {
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte(tag))
			}(c)
		}
	}()
}

// probe connects to the forwarded port on the server and returns the tag the
// answering client sent.
func probe(t *testing.T, serverAddr string, port uint32) string {
	t.Helper()
	host, _, _ := net.SplitHostPort(serverAddr)
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, _ := io.ReadAll(c)
	return string(b)
}

// --- tests ---

// TestEvictionReclaimsPort is the core fix: when a client reconnects while its
// previous session is still alive (as happens after a dirty mobile/satellite
// drop the server hasn't noticed yet), the new session evicts the old one and
// takes over the same forwarded port, instead of failing the forward.
func TestEvictionReclaimsPort(t *testing.T) {
	old := EvictWait
	EvictWait = 2 * time.Second
	defer func() { EvictWait = old }()

	key := newSigner(t)
	port := freePort(t)
	addr, stop := startTestServer(t, key.PublicKey(), "tk-test", port)
	defer stop()

	// Client 1 connects and forwards the port.
	c1, raw1 := dialClient(t, addr, key)
	l1, err := c1.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		t.Fatalf("client1 forward: %v", err)
	}
	serveEcho(l1, "one")
	if got := probe(t, addr, port); got != "one" {
		t.Fatalf("before reconnect: got %q, want %q", got, "one")
	}

	// Client 2 connects with the SAME name while client 1 is still up, and asks
	// for the SAME port. Without eviction this Listen would fail (port in use).
	c2, _ := dialClient(t, addr, key)
	var l2 net.Listener
	// The reconnect path evicts client1 and waits up to EvictWait; retry briefly.
	deadline := time.Now().Add(EvictWait + time.Second)
	for time.Now().Before(deadline) {
		l2, err = c2.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("client2 could not reclaim port %d after eviction: %v", port, err)
	}
	serveEcho(l2, "two")

	if got := probe(t, addr, port); got != "two" {
		t.Fatalf("after reconnect: got %q, want %q (port not handed over)", got, "two")
	}
	_ = raw1 // client1 was evicted server-side
	c1.Close()
	c2.Close()
}

// TestCleanDisconnectFreesPort guards against listener leaks: after a client
// disconnects normally, its forwarded port must be free to bind again.
func TestCleanDisconnectFreesPort(t *testing.T) {
	key := newSigner(t)
	port := freePort(t)
	addr, stop := startTestServer(t, key.PublicKey(), "tk-test", port)
	defer stop()

	c1, _ := dialClient(t, addr, key)
	if _, err := c1.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port)); err != nil {
		t.Fatalf("forward: %v", err)
	}
	c1.Close() // clean SSH disconnect

	// The port should become bindable again shortly.
	freed := false
	for i := 0; i < 40; i++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port)); err == nil {
			l.Close()
			freed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !freed {
		t.Fatalf("port %d still bound after clean disconnect (listener leak)", port)
	}
}

// TestServerSendsKeepalive verifies the server probes the client with
// keepalive@openssh.com (the mechanism that detects dead links and frees ports).
func TestServerSendsKeepalive(t *testing.T) {
	oldKA := KeepaliveInterval
	KeepaliveInterval = 100 * time.Millisecond
	defer func() { KeepaliveInterval = oldKA }()

	key := newSigner(t)
	addr, stop := startTestServer(t, key.PublicKey(), "tk-test")
	defer stop()

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ClientConfig{
		User:            "tunnel",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	go func() {
		for range chans { // reject nothing; just drain
		}
	}()

	got := make(chan string, 1)
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(false, nil)
			}
			select {
			case got <- r.Type:
			default:
			}
		}
	}()

	select {
	case typ := <-got:
		if typ != "keepalive@openssh.com" {
			t.Fatalf("got global request %q, want keepalive@openssh.com", typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never sent a keepalive")
	}
}

// TestForwardNotAllowed guards the existing permitlisten behaviour: a port not
// in the key's allowlist must be refused.
func TestForwardNotAllowed(t *testing.T) {
	key := newSigner(t)
	allowed := freePort(t)
	addr, stop := startTestServer(t, key.PublicKey(), "tk-test", allowed)
	defer stop()

	c, _ := dialClient(t, addr, key)
	defer c.Close()
	other := freePort(t)
	if _, err := c.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", other)); err == nil {
		t.Fatalf("forward of disallowed port %d was accepted", other)
	}
}
