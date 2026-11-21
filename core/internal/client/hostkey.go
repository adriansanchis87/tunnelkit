package client

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hostKey is a parsed server public key line ("ssh-ed25519 AAAA... comment").
// Only the standard library is used on purpose: the client stays tiny for
// OpenWrt and the actual verification is done by ssh/dbclient.
type hostKey struct {
	typ  string // e.g. ssh-ed25519
	b64  string // base64 key blob
	hash string // SHA256:... fingerprint, as OpenSSH prints it
}

// parseHostKey validates a public key line and computes its fingerprint.
func parseHostKey(line string) (*hostKey, error) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return nil, fmt.Errorf(`host key must look like "ssh-ed25519 AAAA..." (got %d field(s))`, len(f))
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return nil, fmt.Errorf("host key is not valid base64: %w", err)
	}
	// The blob starts with the key type as an SSH string; it must agree with
	// the first field, which catches a truncated or mismatched paste.
	if len(blob) < 4 {
		return nil, fmt.Errorf("host key blob too short")
	}
	n := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	if n <= 0 || 4+n > len(blob) || string(blob[4:4+n]) != f[0] {
		return nil, fmt.Errorf("host key blob does not match its type %q", f[0])
	}
	sum := sha256.Sum256(blob)
	return &hostKey{
		typ:  f[0],
		b64:  f[1],
		hash: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
	}, nil
}

// knownHostsEntries returns the known_hosts line that pins host:port to this
// key. OpenSSH matches non-22 ports as "[host]:port"; Dropbear does not look at
// the port at all and only matches the bare host (bare=true).
func (k *hostKey) knownHostsEntries(host string, port int, bare bool) string {
	pat := host
	if port != 22 && !bare {
		pat = fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s %s %s\n", pat, k.typ, k.b64)
}

// writeKnownHosts creates a private temp dir with the pinned key and returns
// the known_hosts path and a cleanup func. The file lives at <dir>/.ssh/known_hosts
// so the same dir can be used as HOME for dbclient.
func writeKnownHosts(k *hostKey, host string, port int, bare bool) (dir, path string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "tunnelkit-hk-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	sshDir := filepath.Join(dir, ".ssh")
	if err = os.Mkdir(sshDir, 0o700); err != nil {
		cleanup()
		return "", "", nil, err
	}
	path = filepath.Join(sshDir, "known_hosts")
	if err = os.WriteFile(path, []byte(k.knownHostsEntries(host, port, bare)), 0o600); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return dir, path, cleanup, nil
}
