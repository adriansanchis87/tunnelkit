// Package client keeps the reverse SSH tunnel to the ingress alive.
//
// It does what autossh does, but better for our case: reconnection driven by
// ssh's real exit signal (not a monitor port the server rejects),
// ExitOnForwardFailure so no half-open tunnels are left behind, sane keepalives,
// and a /metrics endpoint the ingress can scrape over a forwarded port.
package client

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adriansanchis87/tunnelkit/internal/config"
	"github.com/adriansanchis87/tunnelkit/internal/speedtest"
	"github.com/adriansanchis87/tunnelkit/internal/stats"
)

// Run starts the tunnel supervisor (blocks until a stop signal).
func Run(cfg *config.Config) error {
	if cfg.Host == "" {
		return fmt.Errorf("missing ingress host (--host / TK_HOST / options.json)")
	}
	st := stats.New(cfg.SpeedtestForwardPort)
	if cfg.StatsAddr != "" {
		go func() {
			if err := st.Serve(cfg.StatsAddr); err != nil {
				log.Printf("stats: %v", err)
			}
		}()
	}
	// Speedtest responder: the ingress will measure against it over the tunnel.
	if cfg.SpeedtestListen != "" {
		go func() {
			if err := speedtest.RunResponder(cfg); err != nil {
				log.Printf("speedtest: %v", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for ctx.Err() == nil {
		start := time.Now()
		cmd := buildSSH(ctx, cfg)
		log.Printf("tunnelkit: connecting to %s@%s:%d", cfg.User, cfg.Host, cfg.SSHPort)

		if err := cmd.Start(); err != nil {
			st.RecordDisconnect(0, err)
			log.Printf("tunnelkit: ssh failed to start: %v", err)
		} else {
			st.SetConnected()
			err := cmd.Wait()
			st.RecordDisconnect(time.Since(start), err)
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("tunnelkit: ssh exited after %s (%v)", time.Since(start).Round(time.Second), err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.ReconnectDelay):
		}
	}
	return nil
}

// buildSSH builds the ssh -N -R ... command (openssh or dropbear).
func buildSSH(ctx context.Context, cfg *config.Config) *exec.Cmd {
	var args []string
	if cfg.SSHImpl == "dropbear" {
		args = dropbearArgs(cfg)
	} else {
		args = opensshArgs(cfg)
	}
	cmd := exec.CommandContext(ctx, cfg.SSHCommand, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// forwards returns the common -R flags (tunnels + stats + speedtest).
func forwards(cfg *config.Config) []string {
	var r []string
	for _, f := range cfg.Forwards {
		r = append(r, "-R", f)
	}
	if cfg.StatsForwardPort > 0 { // client /metrics toward the ingress
		r = append(r, "-R", fmt.Sprintf("%d:%s", cfg.StatsForwardPort, hostify(cfg.StatsAddr)))
	}
	if cfg.SpeedtestForwardPort > 0 { // speedtest responder toward the ingress
		r = append(r, "-R", fmt.Sprintf("%d:%s", cfg.SpeedtestForwardPort, hostify(cfg.SpeedtestListen)))
	}
	return r
}

func opensshArgs(cfg *config.Config) []string {
	args := []string{
		"-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=" + strconv.Itoa(cfg.ServerAliveInterval),
		"-o", "ServerAliveCountMax=" + strconv.Itoa(cfg.ServerAliveCountMax),
		"-p", strconv.Itoa(cfg.SSHPort),
	}
	if cfg.KeyFile != "" {
		args = append(args, "-i", cfg.KeyFile)
	}
	args = append(args, forwards(cfg)...)
	if cfg.ExtraSSHOpts != "" {
		args = append(args, strings.Fields(cfg.ExtraSSHOpts)...)
	}
	return append(args, cfg.User+"@"+cfg.Host)
}

// dropbearArgs builds the arguments for dbclient (Dropbear, OpenWrt):
// -y accepts the host key, -K sets keepalive. There is no ExitOnForwardFailure
// (the supervisor retries on exit anyway).
func dropbearArgs(cfg *config.Config) []string {
	args := []string{"-N", "-T", "-y", "-p", strconv.Itoa(cfg.SSHPort)}
	if cfg.KeyFile != "" {
		args = append(args, "-i", cfg.KeyFile)
	}
	if cfg.ServerAliveInterval > 0 {
		args = append(args, "-K", strconv.Itoa(cfg.ServerAliveInterval))
	}
	args = append(args, forwards(cfg)...)
	if cfg.ExtraSSHOpts != "" {
		args = append(args, strings.Fields(cfg.ExtraSSHOpts)...)
	}
	return append(args, cfg.User+"@"+cfg.Host)
}

// hostify turns ":9100" into "localhost:9100" for the -R flag.
func hostify(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
