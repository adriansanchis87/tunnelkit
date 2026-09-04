// Package config loads the configuration from (in order of priority):
// defaults -> /data/options.json (HA addon) -> TK_* environment variables ->
// command-line flags.
package config

import (
	"encoding/json"
	"flag"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the effective configuration for any subcommand.
type Config struct {
	// --- SSH tunnel (client subcommand) ---
	Host                string   // ingress host (sshtunnelserver)
	SSHPort             int      // ingress SSH port
	User                string   // SSH user (e.g. "tunnel")
	KeyFile             string   // private key
	Forwards            []string // remote "-R" forwards: "8060:localhost:8123"
	ServerAliveInterval int
	ServerAliveCountMax int
	ExtraSSHOpts        string        // extra ssh options, space-separated
	ReconnectDelay      time.Duration // wait between retries
	SSHImpl             string        // "openssh" (default) or "dropbear" (OpenWrt)
	SSHCommand          string        // ssh binary to use (default "ssh")

	// --- stats ---
	StatsAddr        string // where the client serves /metrics (":9100")
	StatsForwardPort int    // remote port to expose /metrics to the ingress (0=off)

	// --- speedtest (between client and server, OVER THE TUNNEL) ---
	// The client starts the RESPONDER on SpeedtestListen and forwards it over the
	// tunnel with -R SpeedtestForwardPort. The INITIATOR (on the ingress) measures
	// against sshtunnelserver:<SpeedtestForwardPort>, pointing SpeedtestAddr there.
	SpeedtestListen      string // client's local responder (":9102")
	SpeedtestForwardPort int    // responder's remote -R port (0=off)
	SpeedtestAddr        string // initiator: "host:port" destination to measure
	SpeedtestBytes       int64  // bytes per direction in each test

	// --- tunnel server (server subcommand) ---
	ServerSSHAddr        string // where the server listens for SSH (":2223")
	ServerHostKey        string // host key file (created if it does not exist)
	ServerAuthorizedKeys string // authorized_keys with permitlisten
	ServerMonitorAddr    string // web panel (empty = off, e.g. ":9090")
	ServerTrafficFile    string // per-port/per-day traffic persistence
}

const optionsFile = "/data/options.json"

func defaults() *Config {
	return &Config{
		SSHPort:              2222,
		User:                 "tunnel",
		ServerAliveInterval:  20,
		ServerAliveCountMax:  3,
		ReconnectDelay:       5 * time.Second,
		SSHImpl:              "openssh",
		SSHCommand:           "ssh",
		StatsAddr:            ":9100",
		SpeedtestListen:      ":9102",
		SpeedtestBytes:       50 << 20, // 50 MiB
		ServerSSHAddr:        ":2223",
		ServerHostKey:        "/data/tunnelkit_host_key",
		ServerAuthorizedKeys: "/data/authorized_keys",
		ServerTrafficFile:    "/data/traffic.json",
	}
}

// haOptions maps /data/options.json (same names as the addon schema).
type haOptions struct {
	Host                 *string  `json:"host"`
	SSHPort              *int     `json:"ssh_port"`
	User                 *string  `json:"username"`
	KeyFile              *string  `json:"key_file"`
	Forwards             []string `json:"remote_forwarding"`
	ServerAliveInterval  *int     `json:"server_alive_interval"`
	ServerAliveCountMax  *int     `json:"server_alive_count_max"`
	ExtraSSHOpts         *string  `json:"other_ssh_options"`
	StatsForwardPort     *int     `json:"stats_forward_port"`
	SpeedtestForwardPort *int     `json:"speedtest_forward_port"`
}

// Load builds the Config by applying the four layers.
func Load(args []string) *Config {
	c := defaults()
	c.applyOptionsFile()
	c.applyEnv()
	c.applyFlags(args)
	return c
}

func (c *Config) applyOptionsFile() {
	data, err := os.ReadFile(optionsFile)
	if err != nil {
		return // not an HA addon, or no options: ignore
	}
	var o haOptions
	if json.Unmarshal(data, &o) != nil {
		return
	}
	if o.Host != nil {
		c.Host = *o.Host
	}
	if o.SSHPort != nil {
		c.SSHPort = *o.SSHPort
	}
	if o.User != nil {
		c.User = *o.User
	}
	if o.KeyFile != nil {
		c.KeyFile = *o.KeyFile
	}
	if o.Forwards != nil {
		c.Forwards = o.Forwards
	}
	if o.ServerAliveInterval != nil {
		c.ServerAliveInterval = *o.ServerAliveInterval
	}
	if o.ServerAliveCountMax != nil {
		c.ServerAliveCountMax = *o.ServerAliveCountMax
	}
	if o.ExtraSSHOpts != nil {
		c.ExtraSSHOpts = *o.ExtraSSHOpts
	}
	if o.StatsForwardPort != nil {
		c.StatsForwardPort = *o.StatsForwardPort
	}
	if o.SpeedtestForwardPort != nil {
		c.SpeedtestForwardPort = *o.SpeedtestForwardPort
	}
}

func (c *Config) applyEnv() {
	setStr(&c.Host, "TK_HOST")
	setInt(&c.SSHPort, "TK_SSH_PORT")
	setStr(&c.User, "TK_USER")
	setStr(&c.KeyFile, "TK_KEY_FILE")
	if v := os.Getenv("TK_FORWARDS"); v != "" {
		c.Forwards = splitList(v)
	}
	setInt(&c.ServerAliveInterval, "TK_SERVER_ALIVE_INTERVAL")
	setInt(&c.ServerAliveCountMax, "TK_SERVER_ALIVE_COUNT_MAX")
	setStr(&c.ExtraSSHOpts, "TK_EXTRA_SSH_OPTS")
	setStr(&c.SSHImpl, "TK_SSH_IMPL")
	setStr(&c.SSHCommand, "TK_SSH_COMMAND")
	setStr(&c.StatsAddr, "TK_STATS_ADDR")
	setInt(&c.StatsForwardPort, "TK_STATS_FORWARD_PORT")
	setStr(&c.SpeedtestListen, "TK_SPEEDTEST_LISTEN")
	setInt(&c.SpeedtestForwardPort, "TK_SPEEDTEST_FORWARD_PORT")
	setStr(&c.SpeedtestAddr, "TK_SPEEDTEST_ADDR")
	setStr(&c.ServerSSHAddr, "TK_SERVER_SSH_ADDR")
	setStr(&c.ServerHostKey, "TK_SERVER_HOST_KEY")
	setStr(&c.ServerAuthorizedKeys, "TK_SERVER_AUTHORIZED_KEYS")
	setStr(&c.ServerMonitorAddr, "TK_SERVER_MONITOR_ADDR")
	setStr(&c.ServerTrafficFile, "TK_SERVER_TRAFFIC_FILE")
}

func (c *Config) applyFlags(args []string) {
	fs := flag.NewFlagSet("tunnelkit", flag.ContinueOnError)
	host := fs.String("host", c.Host, "ingress host")
	port := fs.Int("ssh-port", c.SSHPort, "ingress SSH port")
	user := fs.String("user", c.User, "SSH user")
	key := fs.String("key", c.KeyFile, "private key")
	fwd := fs.String("forwards", strings.Join(c.Forwards, ","), "comma-separated -R forwards")
	sai := fs.Int("alive-interval", c.ServerAliveInterval, "ServerAliveInterval")
	sacm := fs.Int("alive-count", c.ServerAliveCountMax, "ServerAliveCountMax")
	extra := fs.String("ssh-opts", c.ExtraSSHOpts, "extra ssh options")
	sshImpl := fs.String("ssh-impl", c.SSHImpl, "openssh | dropbear (OpenWrt)")
	sshCmd := fs.String("ssh-command", c.SSHCommand, "ssh binary to use")
	statsAddr := fs.String("stats-addr", c.StatsAddr, "addr for /metrics")
	statsFwd := fs.Int("stats-forward", c.StatsForwardPort, "remote port for /metrics (0=off)")
	stListen := fs.String("speedtest-listen", c.SpeedtestListen, "client's local responder")
	stFwd := fs.Int("speedtest-forward", c.SpeedtestForwardPort, "responder's remote -R port (0=off)")
	stAddr := fs.String("speedtest-addr", c.SpeedtestAddr, "initiator: host:port destination to measure")
	stBytes := fs.Int64("speedtest-bytes", c.SpeedtestBytes, "bytes per direction in the speedtest")
	srvAddr := fs.String("server-ssh-addr", c.ServerSSHAddr, "server: where SSH listens")
	srvHK := fs.String("server-host-key", c.ServerHostKey, "server: host key file")
	srvAK := fs.String("server-authorized-keys", c.ServerAuthorizedKeys, "server: authorized_keys")
	srvMon := fs.String("server-monitor-addr", c.ServerMonitorAddr, "server: web panel (empty=off)")
	if fs.Parse(args) != nil {
		return
	}
	c.ServerSSHAddr, c.ServerHostKey, c.ServerAuthorizedKeys = *srvAddr, *srvHK, *srvAK
	c.ServerMonitorAddr = *srvMon
	c.Host, c.SSHPort, c.User, c.KeyFile = *host, *port, *user, *key
	if *fwd != "" {
		c.Forwards = splitList(*fwd)
	}
	c.ServerAliveInterval, c.ServerAliveCountMax = *sai, *sacm
	c.ExtraSSHOpts = *extra
	c.SSHImpl, c.SSHCommand = *sshImpl, *sshCmd
	c.StatsAddr, c.StatsForwardPort = *statsAddr, *statsFwd
	c.SpeedtestListen, c.SpeedtestForwardPort = *stListen, *stFwd
	c.SpeedtestAddr, c.SpeedtestBytes = *stAddr, *stBytes
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func setStr(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func setInt(dst *int, env string) {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}
