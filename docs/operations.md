# Operations

## What the server stores

Everything lives in the `/data` volume:

| File | What | Loss means |
|---|---|---|
| `host_key` | the server's SSH identity (private) | every client with a pinned `host_key` refuses the server until you update it; unpinned clients with a stored known_hosts refuse it too |
| `authorized_keys` | allowed client keys and their ports | no client can connect until you recreate it |
| `clients.json` | optional titles, port names and expected URLs for the panel | only the friendly names are lost |
| `traffic.json` | per-port / per-day traffic history | only the history is lost |

### Backups

`host_key` is the critical one: it is a private key and cannot be recreated
without touching every client. Back it up and keep the backup private.

```bash
tar czf tunnelkit-backup-$(date +%F).tgz -C data host_key authorized_keys traffic.json clients.json   # drop clients.json if you do not use it
chmod 600 tunnelkit-backup-*.tgz
# encrypt it before it leaves the host, e.g. gpg -c / age
```

To restore, put the files back in the volume with the same ownership and
permissions (`host_key` must stay private, mode `600`) and start the server.

Bring-up of a replacement server with the same `host_key` is transparent to
clients; with a new one they must be re-pinned (see
[Key rotation](key-rotation.md#server-host-key)).

## Adding and revoking clients

Edit `data/authorized_keys`; the server notices the change on the next
authentication. Mount the **directory** (`./data:/data`), not the single file, so
that editors which replace the file are seen by the container.

- **Add:** append a line (see [Getting started](getting-started.md#3-authorize-the-client-on-the-server)).
- **Change the ports of a client:** edit its `permitlisten` list. Permissions are
  fixed when a client authenticates, so the change applies when it next
  reconnects.
- **Revoke:** delete the line. This stops new connections, but a session that is
  already open **stays up** until it disconnects. To cut it off immediately,
  restart the server (`docker restart tunnelkit-server`; clients reconnect on
  their own within seconds) after removing the key.

## Exposing the monitor panel

The panel and its API (`/api/status`, `/api/traffic`, `/api/uptime`,
`/api/probe`, `/api/speedtest`) have **no authentication of their own**, and the
speedtest endpoint generates load (the probe is rate-limited and can only reach
ports a connected client forwards). Treat `TK_SERVER_MONITOR_ADDR` as an internal port:

- Publish it on loopback only (`"127.0.0.1:9090:9090"`) or keep it on the private
  Docker network.
- If it must be reachable by people, put it behind a reverse proxy that does the
  authentication (Authelia, an OIDC proxy, basic auth...) and TLS.

The SSH port (`2223`) is the one that faces the Internet; it only accepts the keys
in `authorized_keys`, and only the ports each key lists.

## Upgrades

- Pin an exact image tag (`X.Y.Z`) in production; `latest` only moves on releases
  and `edge` moves on every push to `main`.
- Optionally verify the image signature before deploying (see the README).
- Read the [CHANGELOG](../CHANGELOG.md) first: breaking changes are listed there.
- Upgrading the server keeps `/data`, so host key and clients are unaffected.
  Clients reconnect by themselves when the server restarts (a few seconds).
- To roll back, deploy the previous tag.

## Planning ports

Each forwarded port belongs to one client and is listed in its `permitlisten`.
Pick a scheme and keep it (for example one block per site: `8100-8109` for site 1)
so that `authorized_keys` stays readable and two clients never collide. The server
refuses a forward for a port the key does not list.
