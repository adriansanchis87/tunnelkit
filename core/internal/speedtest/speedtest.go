// Package speedtest measures client<->ingress throughput OVER THE TUNNEL (not
// internet speed). It measures BY DURATION (not by a fixed number of bytes) so
// the result is real on both slow and fast links, and starts the timer on the
// FIRST byte so it does not count the satellite latency:
//
//	client -> "GET <ms>\n" : the server sends zeros for ms and closes (measures download)
//	client -> "PUT <ms>\n" : the client sends zeros for ms and closes its write
//	                          side; the server replies "OK <bytes>\n"  (measures upload)
package speedtest

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adriansanchis87/tunnelkit/internal/config"
)

// defaultDur is the measurement window per direction. ~4 s leaves room for TCP
// to exit slow-start and makes latency (timer starts on the first byte) marginal.
const defaultDur = 4 * time.Second

// bufSize: size of the transfer blocks.
const bufSize = 64 << 10

// RunResponder starts the speedtest responder (blocking). It runs on the CLIENT
// and is forwarded over the tunnel; the initiator (ingress) measures against it.
func RunResponder(cfg *config.Config) error {
	ln, err := net.Listen("tcp", cfg.SpeedtestListen)
	if err != nil {
		return err
	}
	log.Printf("speedtest: responder listening on %s", cfg.SpeedtestListen)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go serve(conn)
	}
}

func serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	// A SINGLE bufio.Reader for both the line AND the PUT payload.
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	verb, ms, ok := parseCmd(line)
	if !ok {
		return
	}
	dur := time.Duration(ms) * time.Millisecond
	switch verb {
	case "GET":
		streamFor(conn, dur) // send zeros for dur -> client's download
	case "PUT":
		// Receive and discard until the initiator closes the connection. We do NOT
		// reply anything and do NOT rely on half-close (dropbear does NOT propagate
		// it on a -R): the initiator measures by the bytes it sent.
		_, _ = io.Copy(io.Discard, br)
	}
}

// streamFor writes zeros to w until dur runs out, and returns the bytes
// written. It finishes the in-flight write before stopping.
func streamFor(w io.Writer, dur time.Duration) int64 {
	buf := make([]byte, bufSize) // zeros
	deadline := time.Now().Add(dur)
	var sent int64
	for time.Now().Before(deadline) {
		n, err := w.Write(buf)
		sent += int64(n)
		if err != nil {
			break
		}
	}
	return sent
}

// streams is the number of PARALLEL flows per direction. On high-latency links
// (satellite ~600ms) a single flow is capped by window/RTT (~30 Mbps with
// openssh's channel window; <1 with dropbear's): it is the bandwidth-delay
// product, not the link's capacity. Several flows at once fill the link, just
// like speedtest.net does, and yield the real speed.
const streams = 8

// Measure measures the CLIENT's download and upload (Mbit/s), each direction for
// dur, aggregating `streams` parallel flows. Mind the perspective: the responder
// runs on the CLIENT and the initiator on the SERVER. So the client's DOWNLOAD
// = server->client data (the initiator SENDS, PUT), and the client's UPLOAD =
// client->server data (the initiator RECEIVES, GET).
func Measure(addr string, dur time.Duration) (down, up float64, err error) {
	if dur <= 0 {
		dur = defaultDur
	}
	down, err = runParallel(addr, dur, streamClientDown) // server->client (PUT)
	if err != nil {
		return 0, 0, fmt.Errorf("download: %w", err)
	}
	up, err = runParallel(addr, dur, streamClientUp) // client->server (GET)
	if err != nil {
		return down, 0, fmt.Errorf("upload: %w", err)
	}
	return down, up, nil
}

// runParallel launches `streams` flows at once and aggregates: it sums the bytes
// of all of them and divides by the largest individual duration (they overlap).
// It tolerates a flow failing (e.g. if dropbear limits channels): as long as one
// works, there is a number.
func runParallel(addr string, dur time.Duration, one func(string, time.Duration) (int64, time.Duration, error)) (float64, error) {
	type res struct {
		bytes int64
		dur   time.Duration
		err   error
	}
	out := make([]res, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, d, e := one(addr, dur)
			out[i] = res{b, d, e}
		}(i)
	}
	wg.Wait()
	var total int64
	var maxDur time.Duration
	var firstErr error
	ok := 0
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		total += r.bytes
		if r.dur > maxDur {
			maxDur = r.dur
		}
		ok++
	}
	if ok == 0 {
		return 0, firstErr
	}
	return mbps(total, maxDur), nil
}

// RunInitiator measures and prints it. It runs on the ingress, pointing at the
// client's forwarded port.
func RunInitiator(cfg *config.Config) error {
	if cfg.SpeedtestAddr == "" {
		return fmt.Errorf("missing speedtest destination (--speedtest-addr host:port)")
	}
	down, up, err := Measure(cfg.SpeedtestAddr, defaultDur)
	if err != nil {
		return err
	}
	fmt.Printf("speedtest (%s):\n  download: %6.1f Mbit/s\n  upload: %6.1f Mbit/s\n",
		cfg.SpeedtestAddr, down, up)
	return nil
}

// streamClientUp is ONE flow of the client's UPLOAD (client->server): it sends
// "GET <ms>", counts what it receives, and times from the FIRST byte (so it does
// not count the tunnel's one-way latency). Returns (bytes, duration).
func streamClientUp(addr string, dur time.Duration) (int64, time.Duration, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dur + 30*time.Second))
	if _, err := fmt.Fprintf(conn, "GET %d\n", dur.Milliseconds()); err != nil {
		return 0, 0, err
	}
	buf := make([]byte, bufSize)
	var got int64
	var t0 time.Time
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if t0.IsZero() {
				t0 = time.Now() // start on the first byte
			}
			got += int64(n)
		}
		if err != nil {
			break // EOF when the responder closes
		}
	}
	return got, time.Since(t0), nil
}

// streamClientDown is ONE flow of the client's DOWNLOAD (server->client): it
// sends "PUT <ms>" and zeros for dur, measuring by the bytes it manages to write
// (TCP backpressure makes the write rate ~= the real throughput). It does not
// wait for a response or use half-close, so it also works over dropbear (which
// does not propagate the EOF of a -R). Returns (bytes, duration).
func streamClientDown(addr string, dur time.Duration) (int64, time.Duration, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dur + 30*time.Second))
	if _, err := fmt.Fprintf(conn, "PUT %d\n", dur.Milliseconds()); err != nil {
		return 0, 0, err
	}
	t0 := time.Now()
	sent := streamFor(conn, dur)
	return sent, time.Since(t0), nil
}

func mbps(bytes int64, d time.Duration) float64 {
	secs := d.Seconds()
	if secs <= 0 {
		return 0
	}
	return float64(bytes) * 8 / 1e6 / secs
}

func parseCmd(line string) (verb string, ms int64, ok bool) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) != 2 {
		return "", 0, false
	}
	ms, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || ms < 0 || ms > 60000 { // defensive cap: 60 s
		return "", 0, false
	}
	return strings.ToUpper(f[0]), ms, true
}
