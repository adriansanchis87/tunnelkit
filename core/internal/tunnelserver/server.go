// Package tunnelserver is tunnelkit's own tunnel SERVER: an embedded SSH server
// that accepts reverse tunnels (-R) from clients and exposes the forwarded
// ports. It replaces sshtunnelserver (linuxserver/openssh-server) with no
// external image dependencies. Public-key auth with the SAME authorized_keys +
// permitlisten="<port>" format you already used.
package tunnelserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/adriansanchis87/tunnelkit/internal/config"
	"github.com/adriansanchis87/tunnelkit/internal/monitor"
)

// Run starts the SSH tunnel server (blocking).
func Run(cfg *config.Config) error {
	hostKey, err := loadOrCreateHostKey(cfg.ServerHostKey)
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}
	auth := &authStore{path: cfg.ServerAuthorizedKeys}
	if _, err := auth.reload(); err != nil {
		return fmt.Errorf("authorized_keys: %w", err)
	}
	log.Printf("tunnelserver: %d authorized keys, host key %s",
		len(auth.keys), ssh.FingerprintSHA256(hostKey.PublicKey()))

	sconf := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			info, ok := auth.lookup(ssh.FingerprintSHA256(key))
			if !ok {
				return nil, fmt.Errorf("unauthorized key")
			}
			return &ssh.Permissions{Extensions: map[string]string{
				"ports": info.Ports, "name": info.Name}}, nil
		},
	}
	sconf.AddHostKey(hostKey)

	// Registry of connected clients + persisted traffic + web panel.
	reg := monitor.NewRegistry()
	store := monitor.NewTrafficStore(cfg.ServerTrafficFile)
	if cfg.ServerMonitorAddr != "" {
		go func() {
			if err := monitor.Serve(cfg.ServerMonitorAddr, reg, store); err != nil {
				log.Printf("monitor: %v", err)
			}
		}()
		log.Printf("tunnelserver: web panel on %s", cfg.ServerMonitorAddr)
	}

	ln, err := net.Listen("tcp", cfg.ServerSSHAddr)
	if err != nil {
		return err
	}
	log.Printf("tunnelserver: listening for SSH on %s", cfg.ServerSSHAddr)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleConn(c, sconf, reg, store)
	}
}

type session struct {
	sconn   *ssh.ServerConn
	allowed map[uint32]bool
	mu      sync.Mutex
	fwds    map[uint32]net.Listener
	reg     *monitor.Registry
	store   *monitor.TrafficStore
	id      string
	name    string
}

func handleConn(c net.Conn, sconf *ssh.ServerConfig, reg *monitor.Registry, store *monitor.TrafficStore) {
	sconn, chans, reqs, err := ssh.NewServerConn(c, sconf)
	if err != nil {
		return // handshake/auth failure (includes scans)
	}
	defer sconn.Close()
	s := &session{
		sconn:   sconn,
		allowed: parsePorts(sconn.Permissions.Extensions["ports"]),
		fwds:    map[uint32]net.Listener{},
		reg:     reg,
		store:   store,
		id:      sconn.RemoteAddr().String(),
		name:    sconn.Permissions.Extensions["name"],
	}
	name := s.name
	ip, _, _ := net.SplitHostPort(sconn.RemoteAddr().String())
	reg.Add(s.id, &monitor.Client{Name: name, IP: ip, Since: time.Now()})
	defer reg.Del(s.id)
	log.Printf("tunnelserver: client %s (%s) authenticated (ports %v)",
		name, sconn.RemoteAddr(), keys(s.allowed))

	// Tunnels only: we reject sessions/shells.
	go func() {
		for nc := range chans {
			nc.Reject(ssh.Prohibited, "tunnels only")
		}
	}()

	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			s.startForward(req)
		case "cancel-tcpip-forward":
			s.cancelForward(req)
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
	s.closeAll()
}

// startForward handles a client's "-R <port>": it opens the listener and
// forwards each incoming connection to the client over a forwarded-tcpip channel.
func (s *session) startForward(req *ssh.Request) {
	var p struct {
		Addr string
		Port uint32
	}
	if ssh.Unmarshal(req.Payload, &p) != nil || !s.allowed[p.Port] {
		if req.WantReply {
			req.Reply(false, nil) // port not allowed by permitlisten
		}
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p.Port))
	if err != nil {
		if req.WantReply {
			req.Reply(false, nil)
		}
		return
	}
	s.mu.Lock()
	s.fwds[p.Port] = ln
	s.mu.Unlock()
	if req.WantReply {
		req.Reply(true, nil)
	}
	if s.reg != nil {
		s.reg.AddPort(s.id, p.Port)
	}
	log.Printf("tunnelserver: %s forwards port %d", s.sconn.RemoteAddr(), p.Port)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.pipe(p.Addr, p.Port, conn)
		}
	}()
}

// countW counts the bytes written, for the traffic statistics.
type countW struct {
	w   io.Writer
	add func(int)
}

func (c countW) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if c.add != nil {
		c.add(n)
	}
	return n, err
}

func (s *session) pipe(bindAddr string, bindPort uint32, c net.Conn) {
	defer c.Close()
	// Traffic/connection counters (client's perspective: rx = receives,
	// tx = sends). They are counted in the session (rate/sparklines) and in the
	// persisted store (by port and by day).
	var cl *monitor.Client
	if s.reg != nil {
		cl = s.reg.Get(s.id)
	}
	if cl != nil {
		cl.ConnOpen()
		defer cl.ConnClose()
	}
	addRx := func(n int) {
		if cl != nil {
			cl.AddRx(n)
		}
		if s.store != nil {
			s.store.Add(s.name, bindPort, 0, uint64(n))
		}
	}
	addTx := func(n int) {
		if cl != nil {
			cl.AddTx(n)
		}
		if s.store != nil {
			s.store.Add(s.name, bindPort, uint64(n), 0)
		}
	}
	oh, op, _ := net.SplitHostPort(c.RemoteAddr().String())
	opn, _ := strconv.Atoi(op)
	payload := ssh.Marshal(&struct {
		Addr     string
		Port     uint32
		OrigAddr string
		OrigPort uint32
	}{bindAddr, bindPort, oh, uint32(opn)})
	ch, chReqs, err := s.sconn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(chReqs)
	go func() { io.Copy(countW{ch, addRx}, c); ch.CloseWrite() }() // incoming -> client
	io.Copy(countW{c, addTx}, ch)                                  // client -> incoming
}

func (s *session) cancelForward(req *ssh.Request) {
	var p struct {
		Addr string
		Port uint32
	}
	ssh.Unmarshal(req.Payload, &p)
	s.mu.Lock()
	if ln, ok := s.fwds[p.Port]; ok {
		ln.Close()
		delete(s.fwds, p.Port)
	}
	s.mu.Unlock()
	if req.WantReply {
		req.Reply(true, nil)
	}
}

func (s *session) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.fwds {
		ln.Close()
	}
}

// --- auth / host key ---

// authStore reloads authorized_keys when its mtime changes, so keys can be added
// or rotated WITHOUT restarting the server (just update the file).
// keyInfo: allowed ports (csv) and name (the key's comment).
type keyInfo struct {
	Ports string
	Name  string
}

type authStore struct {
	path  string
	mu    sync.Mutex
	mtime time.Time
	keys  map[string]keyInfo // fingerprint -> info
}

func (a *authStore) reload() (bool, error) {
	st, err := os.Stat(a.path)
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys != nil && st.ModTime().Equal(a.mtime) {
		return false, nil
	}
	m, err := loadAuthorized(a.path)
	if err != nil {
		return false, err
	}
	a.keys, a.mtime = m, st.ModTime()
	return true, nil
}

func (a *authStore) lookup(fp string) (keyInfo, bool) {
	if changed, err := a.reload(); changed && err == nil {
		log.Printf("tunnelserver: authorized_keys reloaded (%d keys)", len(a.keys))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.keys[fp]
	return v, ok
}

func loadAuthorized(path string) (map[string]keyInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]keyInfo{}
	for len(data) > 0 {
		key, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		data = rest
		var ports []string
		for _, o := range options {
			if strings.HasPrefix(o, "permitlisten=") {
				v := strings.Trim(strings.TrimPrefix(o, "permitlisten="), "\"")
				if _, ps, err := net.SplitHostPort(v); err == nil {
					v = ps // accepts "host:port"
				}
				if _, err := strconv.Atoi(v); err == nil {
					ports = append(ports, v)
				}
			}
		}
		out[ssh.FingerprintSHA256(key)] = keyInfo{Ports: strings.Join(ports, ","), Name: comment}
	}
	return out, nil
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(data)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "tunnelkit")
	if err != nil {
		return nil, err
	}
	if path != "" {
		_ = os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	}
	return ssh.NewSignerFromKey(priv)
}

func parsePorts(csv string) map[uint32]bool {
	m := map[uint32]bool{}
	for _, p := range strings.Split(csv, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			m[uint32(n)] = true
		}
	}
	return m
}

func keys(m map[uint32]bool) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
