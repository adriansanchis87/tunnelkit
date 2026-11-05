package monitor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAttemptsDiagnose(t *testing.T) {
	a := NewAttempts()
	down := time.Now().Add(-time.Minute).Unix()

	// Nothing arrived from the client's IP since it went down.
	txt, ago, _, fails := a.Diagnose("1.2.3.4", down)
	if ago != -1 || fails != 0 || !strings.Contains(txt, "no connection from 1.2.3.4 has reached the server") {
		t.Fatalf("no-attempts diagnosis wrong: %q ago=%v fails=%d", txt, ago, fails)
	}

	// Two failed handshakes arrive.
	a.Record("1.2.3.4", "tk-x-web", 15*time.Second, errors.New("no complete SSH handshake"), true, false)
	a.Record("1.2.3.4", "tk-x-web", 15*time.Second, errors.New("no complete SSH handshake"), true, false)
	txt, ago, lerr, fails := a.Diagnose("1.2.3.4", down)
	if fails != 2 || ago < 0 || lerr == "" || !strings.Contains(txt, "reached the server and failed") {
		t.Fatalf("failing diagnosis wrong: %q ago=%v fails=%d", txt, ago, fails)
	}
	if got := a.Recent(10); len(got) != 2 || got[0].Client != "tk-x-web" {
		t.Fatalf("recent = %+v", got)
	}

	// Success resets the failures.
	a.Record("1.2.3.4", "tk-x-web", time.Second, nil, false, false)
	if txt, _, _, fails = a.Diagnose("1.2.3.4", down); fails != 0 || !strings.Contains(txt, "connected again") {
		t.Fatalf("after success: %q fails=%d", txt, fails)
	}
}

func TestAttemptsTrivialAndRateLimit(t *testing.T) {
	a := NewAttempts()
	// A trivial failure (early EOF) refreshes "seen" but is not kept.
	a.Record("9.9.9.9", "", time.Millisecond, errors.New("EOF"), false, true)
	if len(a.Recent(5)) != 0 {
		t.Fatal("trivial failure must not be kept")
	}
	// Unknown IP: first failure is logged, the next ones are suppressed...
	if logIt, _ := a.Record("8.8.8.8", "", time.Second, errors.New("x"), false, false); !logIt {
		t.Fatal("first failure must be logged")
	}
	for i := 0; i < 3; i++ {
		if logIt, _ := a.Record("8.8.8.8", "", time.Second, errors.New("x"), false, false); logIt {
			t.Fatal("repeat from an unknown IP must be suppressed")
		}
	}
	// ...but a known client is always logged, and reports the suppressed count.
	if logIt, n := a.Record("8.8.8.8", "tk-y-web", time.Second, errors.New("x"), true, false); !logIt || n != 3 {
		t.Fatalf("known client: logIt=%v suppressed=%d", logIt, n)
	}
}

func TestRegistryClientAt(t *testing.T) {
	r := NewRegistry()
	r.Add("a", &Client{Name: "tk-c-web", IP: "5.5.5.5"})
	r.Add("b", &Client{Name: "tk-c-ssh", IP: "5.5.5.5"})
	if got := r.ClientAt("5.5.5.5"); got != "tk-c-ssh/tk-c-web" {
		t.Fatalf("ClientAt = %q", got)
	}
	if r.LastIP("tk-c-web") != "5.5.5.5" || r.ClientAt("6.6.6.6") != "" {
		t.Fatal("LastIP/ClientAt wrong")
	}
}

func TestUDPDiagnosis(t *testing.T) {
	if got := udpDiagnosis(nil, ""); !strings.Contains(got, "no UDP heartbeat") {
		t.Fatal(got)
	}
	a := 8.0
	if got := udpDiagnosis(&a, "1.2.3.4"); !strings.Contains(got, "ALIVE") {
		t.Fatal(got)
	}
	b := 600.0
	if got := udpDiagnosis(&b, "1.2.3.4"); !strings.Contains(got, "not reaching") {
		t.Fatal(got)
	}
}
