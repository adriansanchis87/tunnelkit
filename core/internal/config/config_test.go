package config

import "testing"

// withExit swaps the exit hook and returns what it was called with (-1 = never).
func withExit(t *testing.T) *int {
	t.Helper()
	code := -1
	old := exit
	exit = func(c int) { code = c }
	t.Cleanup(func() { exit = old })
	return &code
}

func TestUnknownFlagExits(t *testing.T) {
	code := withExit(t)
	Load([]string{"--hostkey", "ssh-ed25519 AAAA"}) // typo of --host-key
	if *code != 2 {
		t.Errorf("unknown flag: exit code %d, want 2 (it must not start unpinned)", *code)
	}
}

func TestHelpExitsZero(t *testing.T) {
	code := withExit(t)
	Load([]string{"--help"})
	if *code != 0 {
		t.Errorf("--help: exit code %d, want 0", *code)
	}
}

func TestValidFlagsDoNotExit(t *testing.T) {
	code := withExit(t)
	c := Load([]string{"--host", "example.com", "--host-key", "ssh-ed25519 AAAA"})
	if *code != -1 {
		t.Errorf("valid flags exited with %d", *code)
	}
	if c.Host != "example.com" || c.HostKey != "ssh-ed25519 AAAA" {
		t.Errorf("config = %+v", c)
	}
}
