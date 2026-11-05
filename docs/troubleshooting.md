# Troubleshooting

How to find out why a client is not connected, and what the server does on its own to recover.

## Diagnosing a client that will not connect

When a client is offline, the server records what reached it from the client's
last known IP, so "nothing arrives" can be told apart from "connections arrive
and fail":

- `GET /api/status` → each **offline** client row carries `last_ip`,
  `last_attempt_seconds_ago`, `last_attempt_error`, `failed_attempts` and a
  plain-text `diagnosis` (e.g. *no connection from X has reached the server
  since the client went down* vs *3 connection(s) from X reached the server and
  failed; last 40s ago: no complete SSH handshake within 15s*). The top-level
  `failed_handshakes` lists the 20 most recent failed handshakes, newest first.
- The server log prints every failed handshake from a known client's IP
  (`handshake failed from … after …: …`); failures from unknown IPs (port
  scanners) are rate-limited to one line per IP every 5 minutes.
- The **client** runs a pre-flight probe before each `ssh` attempt (TCP connect,
  then wait for the server's SSH banner) and logs exactly how far it got, e.g.
  `TCP connected … but no SSH banner within 10s: the connection opens and then
  stalls or is filtered`. When the probe fails, ssh is not started (OpenSSH
  without a connect timeout can hang silently on such a connection). OpenSSH is
  also started with `ConnectTimeout=20`, and the client reports itself as
  connected only after ssh has stayed up longer than that. Disable the probe
  with `TK_NO_PRECHECK=1`; it is skipped automatically when `other_ssh_options`
  use a `Proxy*` option.

**authorized_keys format**: one public key per client, each restricted to the
ports it may forward with `permitlisten="PORT"` (repeat the option for several
ports):

```
permitlisten="8080",permitlisten="8048",permitlisten="8047" ssh-ed25519 AAAA... client-a
permitlisten="9090" ssh-ed25519 AAAA... client-b
```

The speedtest initiator lives in the same image and measures a client through
its forwarded port:

```bash
docker exec tunnelkit-server tunnelkit-server speedtest --speedtest-addr localhost:8047
```

## UDP heartbeat: is only TCP broken?

When a tunnel drops, the SSH session cannot tell you whether the whole path to
the server is dead or only TCP is (a middlebox, a TCP accelerator on a satellite
link...). The optional UDP heartbeat answers that. Each client sends a small
UDP packet every 10 s; the server echoes it and remembers the last one per
client. `/api/status` then shows, for every client, `udp_seconds_ago` and
`udp_ip` next to the TCP state, and offline rows get a sentence in `diagnosis`
such as *"UDP heartbeat is ALIVE (last 8s ago ...): UDP reaches the server while
the TCP tunnel is down"*. The server logs when a client's heartbeats stop or
resume together with whether its TCP tunnel is up.

It is a diagnostic, not a second tunnel, and it does not use any other VPN.

| Side | Variable | Meaning |
|---|---|---|
| server | `TK_SERVER_UDP_ADDR` | UDP listen address (`:2223`); empty = off. Publish it (`2223:2223/udp`) |
| both | `TK_UDP_KEY` | shared key; packets are HMAC-signed, bad ones get no reply |
| client | `TK_UDP_PORT` | server UDP port (`2223`); 0 = off |
| client | `TK_UDP_NAME` | the name the server knows the client by (`tk-<site>-<role>`) |
| client | `TK_UDP_INTERVAL` | seconds between beats (default 10) |

Packets are fixed-size and the reply is as long as the request (no
amplification). The client reports `udp_ok`, `udp_rtt_ms` and `udp_loss_pct` in
`/metrics`. HA add-on options: `udp_heartbeat_port`, `udp_heartbeat_name`,
`udp_heartbeat_key`.

## Stale-session handling (dirty client drops)

When a client drops in the dirty way a mobile/satellite link does (no clean TCP
close), its forwarded listeners would otherwise stay bound for minutes, so the
reconnecting client's `-R` would keep failing until the old socket finally died
(seen as a reconnect loop on OpenSSH clients, or `ports: null` on dropbear ones).
The server guards against this without a restart:

- **Server → client keepalive** (`KeepaliveInterval`, default 20s): probes each
  session with `keepalive@openssh.com`; a failed probe closes the session and
  frees its ports. This is the equivalent of OpenSSH sshd's `ClientAliveInterval`.
  A probe that goes **unanswered** for `KeepaliveTimeout` (default 30s) also
  closes the session: on a half-open link (packets silently swallowed) the probe
  would otherwise block until TCP gave up, ~15 min, with the port still bound.
- **Evict-on-reconnect** (`EvictWait`, default 5s): a new connection for the same
  client name closes the previous session and waits for it to release its
  listeners, so the reconnect reclaims its ports immediately — even before the
  stale TCP has died. (OpenSSH sshd does not do this.)
- TCP keepalive on accepted connections and a handshake deadline as backstops.

Covered by `internal/tunnelserver/server_test.go` (eviction, no listener leak on
clean disconnect, keepalive is sent, unanswered keepalive closes the session and
frees the port, permitlisten still enforced).
