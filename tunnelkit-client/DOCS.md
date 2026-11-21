# TunnelKit Client

A robust reverse SSH tunnel to your server, with `/metrics` and a speedtest.

## Configuration
- **host**: the tunnel server host (tunnelkit-server).
- **ssh_port**: the server's SSH port (default 2222).
- **username**: the SSH user (`tunnel`).
- **key_file**: path to the private key (put it in `/ssl/` or `/config/`).
- **remote_forwarding**: list of `-R` forwards, e.g. `8060:localhost:80`.
- **server_alive_interval / server_alive_count_max**: client keepalive.
- **other_ssh_options**: extra ssh options (optional).
- **host_key**: pins the server's public key (optional but recommended), e.g.
  `ssh-ed25519 AAAA...`. The server prints the exact value at startup
  (`to pin this server on clients set TK_HOST_KEY=...`). When set, any other host
  key is refused; when empty, the first key seen is trusted (TOFU).
- **stats_forward_port**: remote port that exposes `/metrics` to the server (0 = off).

The add-on runs with `boot: auto` and a **watchdog**: if the container dies, the
Supervisor brings it back. It does not use an autossh monitor port (that was the
cause of the restarts): it reconnects on the real ssh exit signal.
