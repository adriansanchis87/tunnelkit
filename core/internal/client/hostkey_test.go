package client

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/adriansanchis87/tunnelkit/internal/config"
)

// A real ed25519 public key and the fingerprint tunnelkit-server printed for
// it, so the fingerprint code is checked against an independent implementation
// (golang.org/x/crypto/ssh).
const (
	testKeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJrt98PsfT5nFT9y5NQsmyLXRohi/UJbsuz5yLjq0e6D"
	testKeyFP   = "SHA256:XrgVhYPpIM2Lzexse4MO+Oxr9WicPoBTKgdM8cPxdrk"
)

func TestParseHostKey(t *testing.T) {
	k, err := parseHostKey(testKeyLine)
	if err != nil {
		t.Fatal(err)
	}
	if k.typ != "ssh-ed25519" {
		t.Errorf("type = %q", k.typ)
	}
	if k.hash != testKeyFP {
		t.Errorf("fingerprint = %q, want %q", k.hash, testKeyFP)
	}
}

func TestParseHostKeyRejects(t *testing.T) {
	for name, line := range map[string]string{
		"empty":         "",
		"only type":     "ssh-ed25519",
		"bad base64":    "ssh-ed25519 !!!notbase64!!!",
		"short blob":    "ssh-ed25519 AAAA",
		"type mismatch": "ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIJrt98PsfT5nFT9y5NQsmyLXRohi/UJbsuz5yLjq0e6D",
		"fingerprint":   testKeyFP,
	} {
		if _, err := parseHostKey(line); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestKnownHostsEntries(t *testing.T) {
	k, _ := parseHostKey(testKeyLine)
	if got := k.knownHostsEntries("example.com", 2223, false); !strings.HasPrefix(got, "[example.com]:2223 ssh-ed25519 ") {
		t.Errorf("non-22 port: %q", got)
	}
	if got := k.knownHostsEntries("example.com", 22, false); !strings.HasPrefix(got, "example.com ssh-ed25519 ") {
		t.Errorf("port 22: %q", got)
	}
	// Dropbear never matches on the port.
	if got := k.knownHostsEntries("example.com", 2223, true); !strings.HasPrefix(got, "example.com ssh-ed25519 ") {
		t.Errorf("dropbear (bare host): %q", got)
	}
}

func TestWriteKnownHosts(t *testing.T) {
	k, _ := parseHostKey(testKeyLine)
	dir, path, cleanup, err := writeKnownHosts(k, "example.com", 2223, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "[example.com]:2223") {
		t.Fatalf("known_hosts = %q, %v", data, err)
	}
	if !strings.HasSuffix(path, "/.ssh/known_hosts") || !strings.HasPrefix(path, dir) {
		t.Errorf("path layout: %s (dir %s)", path, dir)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove %s", dir)
	}
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestOpenSSHArgsPinned(t *testing.T) {
	cfg := &config.Config{Host: "h", User: "u", SSHPort: 2223, SSHImpl: "openssh", SSHCommand: "ssh",
		ServerAliveInterval: 20, ServerAliveCountMax: 3}
	un := opensshArgs(cfg, nil)
	if !hasPair(un, "-o", "StrictHostKeyChecking=accept-new") {
		t.Errorf("unpinned must keep TOFU: %v", un)
	}
	p := opensshArgs(cfg, &pinned{dir: "/d", knownHosts: "/d/.ssh/known_hosts"})
	for _, want := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=/d/.ssh/known_hosts", "GlobalKnownHostsFile=/dev/null"} {
		if !hasPair(p, "-o", want) {
			t.Errorf("pinned missing %s: %v", want, p)
		}
	}
	if hasPair(p, "-o", "StrictHostKeyChecking=accept-new") {
		t.Errorf("pinned must not accept new keys: %v", p)
	}
}

func TestDropbearArgsPinned(t *testing.T) {
	cfg := &config.Config{Host: "h", User: "u", SSHPort: 2223, SSHImpl: "dropbear", SSHCommand: "dbclient", ServerAliveInterval: 20}
	has := func(args []string, f string) bool {
		for _, a := range args {
			if a == f {
				return true
			}
		}
		return false
	}
	if !has(dropbearArgs(cfg, nil), "-y") {
		t.Error("unpinned dropbear must keep -y")
	}
	if has(dropbearArgs(cfg, &pinned{dir: "/d"}), "-y") {
		t.Error("pinned dropbear must not pass -y")
	}
}

func TestBuildSSHDropbearUsesPrivateHome(t *testing.T) {
	cfg := &config.Config{Host: "h", User: "u", SSHPort: 2223, SSHImpl: "dropbear", SSHCommand: "dbclient"}
	cmd := buildSSH(context.Background(), cfg, &pinned{dir: "/tmp/x", knownHosts: "/tmp/x/.ssh/known_hosts"})
	var home string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "HOME=") {
			home = e // last one wins in exec.Cmd
		}
	}
	if home != "HOME=/tmp/x" {
		t.Errorf("HOME = %q", home)
	}
}
