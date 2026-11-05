# Getting started

This sets up one server and one client. The server is a public host that edge
devices dial into; the client runs on the device and exposes a local service
through the server. Replace `example.com` with your server's name.

## 1. Run the server

```bash
mkdir -p data && cp server/docker-compose.yml .   # or copy the file by hand
docker compose up -d
docker compose logs tunnelkit-server
```

On first start the server creates its **host key** at `data/host_key` and logs
how to pin it on clients:

```
tunnelserver: 0 authorized keys, host key SHA256:...
tunnelserver: to pin this server on clients set TK_HOST_KEY="ssh-ed25519 AAAA..." ...
tunnelserver: listening for SSH on :2223
```

Keep that line: you will paste it into each client. **Back up `data/host_key`**
(see [Operations](operations.md#backups)).

Open `2223/tcp` (SSH) in your firewall. The example compose file publishes the
panel (`9090`) on loopback only; do **not** expose it to the Internet as it is, see
[Exposing the monitor panel](operations.md#exposing-the-monitor-panel).

## 2. Create a key for the client

On the client machine (or anywhere, then copy the private key to it):

```bash
ssh-keygen -t ed25519 -N "" -f id_tunnel -C tk-site1-web
```

The **comment** (`tk-site1-web`) is the client's name: it is what the panel and
the logs show, and a second connection with the same name replaces the first.
Use one name per client.

OpenWrt with the default Dropbear backend needs the key in Dropbear format:
`dropbearconvert openssh dropbear id_tunnel id_tunnel.db`. See the OpenWrt
section of the README.

## 3. Authorize the client on the server

Add one line to `data/authorized_keys`, listing every server port this client may
open with `permitlisten`:

```
permitlisten="8080",permitlisten="8048",permitlisten="8047" ssh-ed25519 AAAA... tk-site1-web
```

- `8080`: the service you forward (`-R 8080:localhost:80`).
- `8048` / `8047`: optional ports for the client's `/metrics` and speedtest
  responder, used by the panel (`stats_forward_port`, `speedtest_forward_port`).

The file is re-read automatically when it changes; no restart is needed to add a
client. A port can be bound by only one client at a time, so give every client
its own ports.

## 4. Run the client

```bash
docker run -d --name tunnelkit-client --restart unless-stopped --network host \
  -v "$PWD/id_tunnel:/keys/id_tunnel:ro" \
  -e TK_HOST=example.com -e TK_SSH_PORT=2223 -e TK_USER=tunnel \
  -e TK_KEY_FILE=/keys/id_tunnel \
  -e TK_HOST_KEY="ssh-ed25519 AAAA..." \
  -e TK_FORWARDS="8080:localhost:80" \
  -e TK_STATS_FORWARD_PORT=8048 -e TK_SPEEDTEST_FORWARD_PORT=8047 \
  ghcr.io/adriansanchis87/tunnelkit-client:X.Y.Z
```

Use an exact version tag instead of `latest` (see
[releases](../README.md#releases-and-image-verification)). `TK_HOST_KEY` pins the
server: without it the client trusts the first key it sees. Other packagings
(OpenWrt, Home Assistant add-on) take the same settings; see the README.

## 5. Check that it works

On the server:

```bash
curl -s http://localhost:9090/api/status
```

Your client should appear as connected with its ports. The forwarded port is bound
**inside the server container** on all interfaces, so to use it either publish it
(`ports: ["8080:8080"]` in the server's compose file) or reach it by container name
from another container on the same Docker network (for example a reverse proxy
pointing at `tunnelkit-server:8080`).

If the client does not show up, see [Troubleshooting](troubleshooting.md).
